package main

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"tollgate-auth/internal/auth"
	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/config"
	"tollgate-auth/internal/fakeverity"
	"tollgate-auth/internal/ledger"
	"tollgate-auth/internal/operator"
	"tollgate-auth/internal/radius"
	"tollgate-auth/internal/radiusauth"
	"tollgate-auth/internal/redact"
	"tollgate-auth/internal/sessiond"
)

// --- Configuration ---

const (
	BaseDir          = "/opt/cashu-tollgate"
	WalletDir        = "/var/lib/cashu-wallet"
	TokensLogFile    = BaseDir + "/radius-tokens.log"
	RateSecPerSat    = 10
	LNURLWDefaultSec = 600 // 10 minutes default for lnurlw (until we claim and know the amount)
)

// macPattern validates Calling-Station-Id: hex digits, colons, dashes, or empty.
var macPattern = regexp.MustCompile(`^[0-9a-fA-F:\-\.]*$`)

// testMintPattern matches mint URLs that are allowed for token redemption.
// Accepts all mints — SSRF protection in internal/cashu/mint.go blocks
// private/local IPs. Each session's mint URL is recorded in the ledger
// for accounting and audit purposes.
var testMintPattern = regexp.MustCompile(`.*`)

// Delegated mode configuration (read once at startup).
var (
	authMode    = config.GetEnv("TOLLGATE_AUTH_MODE", "local") // "local" or "delegated"
	sessiondURL = config.GetEnv("TOLLGATE_SESSIOND_URL", "http://127.0.0.1:2121")
	opResolver  *operator.Resolver
)

type LedgerConfig struct {
	Path    string
	Enabled bool
}

var ledgerInstance *ledger.Ledger

func initLedger() LedgerConfig {
	path := config.GetEnv("TOLLGATE_LEDGER_PATH", "")
	if path == "" {
		return LedgerConfig{Enabled: false}
	}
	l, err := ledger.OpenLedger(path)
	if err != nil {
		log.Printf("Warning: failed to open ledger at %s: %v (ledger disabled)", path, err)
		return LedgerConfig{Enabled: false}
	}
	ledgerInstance = l
	return LedgerConfig{Path: path, Enabled: true}
}

func recordLedgerAuth(entry ledger.LedgerEntry) {
	if ledgerInstance == nil {
		return
	}
	if err := ledgerInstance.RecordAuth(entry); err != nil {
		log.Printf("Warning: ledger RecordAuth failed: %v", err)
	}
}

func recordLedgerAccounting(entry ledger.LedgerEntry) {
	if ledgerInstance == nil {
		return
	}
	if err := ledgerInstance.RecordAccounting(entry); err != nil {
		log.Printf("Warning: ledger RecordAccounting failed: %v", err)
	}
}

// replyMessage outputs a Reply-Message attribute to stdout.
// FreeRADIUS exec module with output=reply parses stdout as RADIUS attribute pairs.
// Format: Reply-Message = "value"
func replyMessage(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	// Sanitize the message — no newlines, quotes, or commas that would break RADIUS exec output parsing.
	// FreeRADIUS exec module treats commas as attribute separators in program output,
	// so commas inside quoted values truncate the message and break subsequent attributes.
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", "")
	msg = strings.ReplaceAll(msg, `"`, `'`)
	msg = strings.ReplaceAll(msg, ",", ";")
	fmt.Printf("Reply-Message = \"%s\"\n", msg)
}

// radiusAttr outputs a RADIUS attribute to stdout as an integer pair.
// FreeRADIUS exec module with output_pairs = reply parses these into Access-Accept.
func radiusAttr(name string, value int) {
	fmt.Printf("%s = %d\n", name, value)
}

// emitClass creates an HMAC-signed Class attribute for the session and outputs it.
// The Class attribute ties the session to the operator for accounting (RFC 2865 §5.5).
// tokenHash is the first 16 chars of the SHA256 hash of the payment token.
func emitClass(operatorID, sessionID, tokenHash string, hmacKey []byte) string {
	sc := radius.NewSessionClass(operatorID, sessionID, tokenHash)
	signed, err := sc.HMACSign(hmacKey)
	if err != nil {
		log.Printf("Warning: failed to sign Class: %v", err)
		return ""
	}
	fmt.Printf("Class = \"%s\"\n", signed)
	return signed
}

// isTestMint checks if a mint URL matches the allowed test mint pattern.
func isTestMint(mintURL string) bool {
	return testMintPattern.MatchString(mintURL)
}

// --- Main ---

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	defer func() {
		if ledgerInstance != nil {
			ledgerInstance.Close()
		}
	}()

	lcfg := initLedger()
	_ = lcfg

	var resolverErr error
	opResolver, resolverErr = operator.NewResolver("")
	if resolverErr != nil {
		log.Fatalf("Fatal: failed to initialize operator resolver: %v", resolverErr)
	}

	if len(os.Args) >= 2 && os.Args[1] == "--accounting" {
		handleAccounting()
		return
	}

	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <username> <mac-address> [password] [cleartext-password]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  Called by FreeRADIUS exec module.\n")
		fmt.Fprintf(os.Stderr, "  Accepts payment from username, password, or cleartext-password field:\n")
		fmt.Fprintf(os.Stderr, "    Cashu tokens:  cashuA... or cashuB...\n")
		fmt.Fprintf(os.Stderr, "    LNURL-withdraw: lnurlw... (pass-through, TODO: claim payment)\n")
		os.Exit(1)
	}

	username := os.Args[1]
	mac := os.Args[2]
	password := ""
	if len(os.Args) >= 4 {
		password = os.Args[3]
	}
	clearTextPw := ""
	if len(os.Args) >= 5 {
		clearTextPw = os.Args[4]
	}

	if authMode == "delegated" {
		handleDelegated(username, mac, password, clearTextPw)
		return
	}

	sessions := &SessionStore{Dir: BaseDir + "/radius-sessions"}
	os.MkdirAll(sessions.Dir, 0700)

	if time.Now().UnixNano()%20 == 0 {
		sessions.Cleanup()
	}

	os.MkdirAll(BaseDir, 0755)

	clientIP := config.GetEnv("TOLLGATE_CLIENT_IP", "")
	nasID := config.GetEnv("TOLLGATE_NAS_ID", "")
	opCtx := opResolver.Resolve(clientIP, nasID)

	deps := &Dependencies{
		Sessions:   sessions,
		Verifier:   fakeverity.NewProductionVerifier(WalletDir),
		OperatorID: opCtx.Account.ID,
		HMACKey:    opResolver.HMACKey(),
		AuthMode:   "local",
	}

	result := auth.ProcessAuth(deps, username, mac, password, clearTextPw, "", "")
	emitResult(result)
}

func handleDelegated(username, mac, password, clearTextPw string) {
	if !macPattern.MatchString(mac) {
		log.Printf("Reject: invalid MAC format: %q", redact.LogSafe(redact.Truncate(mac, 32)))
		replyMessage("Rejected: invalid session identifier")
		os.Exit(1)
	}

	sessionID := mac
	if sessionID == "" {
		sessionID = "user:" + username
		log.Printf("Info: no Calling-Station-Id, using username-based session: %q", redact.LogSafe(redact.Truncate(sessionID, 64)))
	}

	sessions := &SessionStore{Dir: BaseDir + "/radius-sessions"}
	os.MkdirAll(sessions.Dir, 0700)

	if time.Now().UnixNano()%20 == 0 {
		sessions.Cleanup()
	}

	clientIP := config.GetEnv("TOLLGATE_CLIENT_IP", "")
	nasID := config.GetEnv("TOLLGATE_NAS_ID", "")
	opCtx := opResolver.Resolve(clientIP, nasID)
	operatorID := opCtx.Account.ID

	cred, found := radiusauth.ExtractPayment(username, password, clearTextPw)

	if !found {
		if handleDelegatedReconnection(sessionID) {
			os.Exit(0)
		}
		log.Printf("Reject: no payment credential and no active session (delegated, mac=%s)", sessionID)
		replyMessage("Rejected: no valid payment credential found")
		recordLedgerAuth(ledger.LedgerEntry{
			EventType:    ledger.EventAuthReject,
			MAC:          sessionID,
			ReplyMessage: "Rejected: no valid payment credential found",
		})
		os.Exit(1)
	}

	switch cred.Type {
	case radiusauth.PaymentCashu:
		handleCashuDelegated(cred, sessionID, sessions, operatorID)
	case radiusauth.PaymentLNURLW:
		os.MkdirAll(BaseDir, 0755)
		fmt.Fprintf(os.Stderr, "LNURLw disabled\n")
	}
}

func handleDelegatedReconnection(sessionID string) bool {
	client := sessiond.NewClient(sessiondURL)
	usage, err := client.GetUsage(sessionID)
	if err != nil {
		log.Printf("Delegated reconnection: GetUsage failed for %s: %v", sessionID, err)
		return false
	}

	parts := strings.SplitN(usage, "/", 2)
	if len(parts) != 2 {
		log.Printf("Delegated reconnection: unexpected usage format %q", usage)
		return false
	}

	usedMs, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		log.Printf("Delegated reconnection: parsing used ms: %v", err)
		return false
	}
	allotmentMs, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		log.Printf("Delegated reconnection: parsing allotment ms: %v", err)
		return false
	}

	if usedMs >= allotmentMs {
		return false
	}

	remainingMs := allotmentMs - usedMs
	remainingSec := int(remainingMs / 1000)
	remainingMin := remainingSec / 60

	log.Printf("Delegated reconnection: session=%s active (%dm remaining), accepting", sessionID, remainingMin)
	replyMessage("Session resumed: %dm remaining (delegated)", remainingMin)
	radiusAttr("Session-Timeout", max(1, remainingSec))
	radiusAttr("Acct-Interim-Interval", 60)
	return true
}

// buildDelegatedReplyMessage formats the Reply-Message for delegated mode,
// showing transparency (sats + effective rate) when enriched fields are available.
func buildDelegatedReplyMessage(state *sessiond.SessionState, minutes int) string {
	if state.CreditAmount > 0 && state.EffectiveRateSecPerSat > 0 {
		return fmt.Sprintf("Valid Cashu token: %d sat → %dm access (%ds/sat)",
			state.CreditAmount, minutes, state.EffectiveRateSecPerSat)
	}
	return fmt.Sprintf("Valid Cashu token: %dm access (delegated)", minutes)
}

func handleCashuDelegated(cred radiusauth.PaymentCredential, sessionID string, sessions *SessionStore, operatorID string) {
	thash := cashu.TokenHash(cred.Value)
	os.MkdirAll(BaseDir, 0755)

	// Check for active session first (fast reconnect path)
	if rec, exists := sessions.Get(sessionID); exists && sessions.IsActive(rec) {
		remaining := time.Until(rec.Started.Add(time.Duration(rec.Duration) * time.Second))
		remainingSec := max(1, int(remaining.Seconds()))
		log.Printf("Reconnect: session=%s (delegated, %ds remaining)", sessionID, remainingSec)
		replyMessage("Session resumed: %dm remaining (delegated)", remainingSec/60)
		radiusAttr("Session-Timeout", remainingSec)
		radiusAttr("Acct-Interim-Interval", 60)
		os.Exit(0)
	}

	client := sessiond.NewClient(sessiondURL)
	state, err := client.Bootstrap(cred.Value, sessionID)
	if err != nil {
		log.Printf("Reject: delegated bootstrap failed (%s): %v", cred.Source, err)
		replyMessage("Rejected: delegated session failed — %v", err)
		os.Exit(1)
	}

	seconds := int(state.AllotmentMs / 1000)
	minutes := seconds / 60

	if seconds <= 0 {
		log.Printf("Reject: delegated bootstrap returned zero allotment for session=%s", sessionID)
		replyMessage("Rejected: zero allotment from server")
		os.Exit(1)
	}

	if existingRec, hasExisting := sessions.Get(sessionID); hasExisting && sessions.IsActive(existingRec) {
		if nasIP := config.GetEnv("TOLLGATE_NAS_IP", ""); nasIP != "" {
			log.Printf("CoA: top-up detected for session=%s, sending CoA to nas=%s", sessionID, nasIP)
			sendCoAOrDisconnect(nasIP, seconds, "", sessionID)
		}
	}

	classStr := emitClass(operatorID, sessionID, thash[:16], opResolver.HMACKey())

	// Use enriched fields when available, fall back gracefully for legacy servers
	var displayAmount int
	var ledgerCreditAmount int
	if state.CreditAmount > 0 {
		displayAmount = int(state.CreditAmount)
		ledgerCreditAmount = int(state.CreditAmount)
	} else {
		displayAmount = minutes
		ledgerCreditAmount = 0
	}

	rec := &SessionRecord{
		MAC:      sessionID,
		Token:    thash,
		Guest:    "radius-delegated-" + thash[:8],
		Mint:     "delegated",
		Amount:   displayAmount,
		Started:  time.Now(),
		Duration: seconds,
		Source:   cred.Source,
		PayType:  radiusauth.PaymentCashu,
		Class:    classStr,
	}
	if err := sessions.Save(rec); err != nil {
		log.Printf("Warning: failed to save session record: %v", err)
	}

	log.Printf("Accept: session=%s type=delegated duration=%ds sats=%d source=%s",
		sessionID, seconds, ledgerCreditAmount, cred.Source)

	recordLedgerAuth(ledger.LedgerEntry{
		EventType:    ledger.EventAuthAccept,
		MAC:          sessionID,
		PaymentType:  "delegated",
		CreditAmount: ledgerCreditAmount,
		DurationSec:  seconds,
		TokenHash:    thash,
		ReplyMessage: buildDelegatedReplyMessage(state, minutes),
	})

	replyMessage("%s", buildDelegatedReplyMessage(state, minutes))
	radiusAttr("Session-Timeout", seconds)
	radiusAttr("Acct-Interim-Interval", 60)
	os.Exit(0)
}
