package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"tollgate-auth/internal/cashu"
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
	RateSecPerSat    = 60
	LNURLWDefaultSec = 3600 // 1 hour default for lnurlw (until we claim and know the amount)
)

// macPattern validates Calling-Station-Id: hex digits, colons, dashes, or empty.
var macPattern = regexp.MustCompile(`^[0-9a-fA-F:\-\.]*$`)

// testMintPattern matches mint URLs that are allowed for token redemption.
// Only mints with "test" in the URL (case-insensitive) are accepted.
// This prevents accidental redemption of tokens with real monetary value.
// Customize this regex to allow additional mints.
var testMintPattern = regexp.MustCompile(`(?i)test`)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Delegated mode configuration (read once at startup).
var (
	authMode    = getEnv("TOLLGATE_AUTH_MODE", "local")       // "local" or "delegated"
	sessiondURL = getEnv("TOLLGATE_SESSIOND_URL", "http://127.0.0.1:2121")
	opResolver  *operator.Resolver
)

type LedgerConfig struct {
	Path    string
	Enabled bool
}

var ledgerInstance *ledger.Ledger

func initLedger() LedgerConfig {
	path := getEnv("TOLLGATE_LEDGER_PATH", "")
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

// SessionStore tracks active RADIUS sessions by MAC address.
// When a phone reconnects (sleep/wake), we skip token validation
// if the session is still within its paid time window.
type SessionStore struct {
	Dir string
}

type SessionRecord struct {
	MAC      string                  `json:"mac"`
	Token    string                  `json:"token_hash"`
	Guest    string                  `json:"guest"`
	Mint     string                  `json:"mint"`
	Amount   int                     `json:"amount"`
	Started  time.Time               `json:"started"`
	Duration int                     `json:"duration"` // seconds
	Source   string                  `json:"source"`   // "username" or "password"
	PayType  radiusauth.PaymentType  `json:"pay_type"` // "cashu" or "lnurlw"
	Class    string                  `json:"class"`
}

func (s *SessionStore) Path(mac string) string {
	clean, ok := radiusauth.SanitizeMAC(mac)
	if !ok {
		clean = cashu.TokenHash(mac)[:16]
	}
	return filepath.Join(s.Dir, clean+".json")
}

func (s *SessionStore) Get(mac string) (*SessionRecord, bool) {
	data, err := os.ReadFile(s.Path(mac))
	if err != nil {
		return nil, false
	}
	var rec SessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, false
	}
	return &rec, true
}

func (s *SessionStore) IsActive(rec *SessionRecord) bool {
	deadline := rec.Started.Add(time.Duration(rec.Duration) * time.Second)
	return time.Now().Before(deadline)
}

func (s *SessionStore) Save(rec *SessionRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path(rec.MAC), data, 0600)
}

func (s *SessionStore) Remove(mac string) {
	os.Remove(s.Path(mac))
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

	replay := cashu.NewReplayGuard(BaseDir + "/radius-spent.txt")
	os.MkdirAll(BaseDir, 0755)

	clientIP := getEnv("TOLLGATE_CLIENT_IP", "")
	nasID := getEnv("TOLLGATE_NAS_ID", "")
	opCtx := opResolver.Resolve(clientIP, nasID)

	deps := &Dependencies{
		Sessions:   sessions,
		Replay:     replay,
		Verifier:   fakeverity.NewProductionVerifier(WalletDir),
		OperatorID: opCtx.Account.ID,
		HMACKey:    opResolver.HMACKey(),
		AuthMode:   "local",
	}

	result := processAuth(deps, username, mac, password, clearTextPw)
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

	clientIP := getEnv("TOLLGATE_CLIENT_IP", "")
	nasID := getEnv("TOLLGATE_NAS_ID", "")
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
		replay := cashu.NewReplayGuard(BaseDir + "/radius-spent.txt")
		os.MkdirAll(BaseDir, 0755)
		handleLNURLw(cred, sessionID, sessions, replay, operatorID)
	}
}

func emitResult(r AuthResult) {
	if r.LogMessage != "" {
		log.Print(r.LogMessage)
	}
	if r.Accept {
		replyMessage("%s", r.ReplyMessage)
		radiusAttr("Session-Timeout", r.SessionTimeout)
		radiusAttr("Acct-Interim-Interval", r.AcctInterval)
		if r.Class != "" {
			fmt.Printf("Class = \"%s\"\n", r.Class)
		}
		os.Exit(0)
	}
	replyMessage("%s", r.ReplyMessage)
	os.Exit(1)
}

func handleCashu(cred radiusauth.PaymentCredential, sessionID string, sessions *SessionStore, replay *cashu.ReplayGuard, operatorID string) {
	tokenData, err := cashu.DecodeToken(cred.Value)
	if err != nil {
		log.Printf("Reject: cashu decode failed (%s): %v", cred.Source, err)
		replyMessage("Rejected: invalid Cashu token format")
		cashu.LogToken(cred.Value, &cashu.TokenData{}, "radius-"+sessionID, false, TokensLogFile)
		os.Exit(1)
	}

	thash := cashu.TokenHash(cred.Value)
	seconds := tokenData.Amount * RateSecPerSat
	minutes := seconds / 60

	if tokenData.Amount <= 0 {
		log.Printf("Reject: zero or negative amount (%d) in token", tokenData.Amount)
		replyMessage("Rejected: token has zero value")
		os.Exit(1)
	}

	// Replay check (atomic check-and-mark)
	if replay.CheckAndMark(thash) {
		log.Printf("Reject: cashu token already spent (hash=%s, source=%s)", thash[:16], cred.Source)
		replyMessage("Rejected: token already used")
		os.Exit(1)
	}

	// Mint allowlist: only accept test mints
	if !isTestMint(tokenData.Mint) {
		log.Printf("Reject: non-test mint (%s) — only test mints are accepted", tokenData.Mint)
		replyMessage("Rejected: mint '%s' is not a testnet mint — only test mints accepted", tokenData.Mint)
		cashu.LogToken(cred.Value, tokenData, "radius-"+sessionID, false, TokensLogFile)
		os.Exit(1)
	}

	ok, msg := cashu.VerifyWithMint(tokenData)
	if !ok {
		log.Printf("Reject: cashu mint verification failed (%s): %s", cred.Source, msg)
		replyMessage("Rejected: mint verification failed — %s", msg)
		cashu.LogTokenWithError(cred.Value, tokenData, "radius-"+sessionID, false, "verify: "+msg, TokensLogFile)
		os.Exit(1)
	}

	if err := cashu.RedeemToken(cred.Value, WalletDir); err != nil {
		log.Printf("Reject: cashu redemption failed (%s): %v", cred.Source, err)
		replyMessage("Rejected: token redemption failed")
		cashu.LogTokenWithError(cred.Value, tokenData, "radius-"+sessionID, false, "redeem: "+err.Error(), TokensLogFile)
		os.Exit(1)
	}

	// Already marked spent by CheckAndMark above
	cashu.LogToken(cred.Value, tokenData, "radius-"+sessionID, true, TokensLogFile)

	// Save session
	classStr := emitClass(operatorID, sessionID, thash[:16], opResolver.HMACKey())
	rec := &SessionRecord{
		MAC:      sessionID,
		Token:    thash,
		Guest:    "radius-" + thash[:8],
		Mint:     tokenData.Mint,
		Amount:   tokenData.Amount,
		Started:  time.Now(),
		Duration: seconds,
		Source:   cred.Source,
		PayType:  radiusauth.PaymentCashu,
		Class:    classStr,
	}
	if err := sessions.Save(rec); err != nil {
		log.Printf("Warning: failed to save session record: %v", err)
	}

	log.Printf("Accept: session=%s type=cashu amount=%d sat duration=%ds mint=%s source=%s",
		sessionID, tokenData.Amount, seconds, tokenData.Mint, cred.Source)

	recordLedgerAuth(ledger.LedgerEntry{
		EventType:   ledger.EventAuthAccept,
		MAC:         sessionID,
		PaymentType: "cashu",
		AmountSat:   tokenData.Amount,
		DurationSec: seconds,
		MintURL:     tokenData.Mint,
		TokenHash:   thash,
		ReplyMessage: fmt.Sprintf("Valid Cashu token: %d sat = %dm access from %s",
			tokenData.Amount, minutes, tokenData.Mint),
	})

	replyMessage("Valid Cashu token: %d sat = %dm access from %s",
		tokenData.Amount, minutes, tokenData.Mint)
	radiusAttr("Session-Timeout", seconds)
	radiusAttr("Acct-Interim-Interval", 60)
	os.Exit(0)
}

// handleLNURLw processes an LNURL-withdraw code.
func handleLNURLw(cred radiusauth.PaymentCredential, sessionID string, sessions *SessionStore, replay *cashu.ReplayGuard, operatorID string) {
	thash := cashu.TokenHash(cred.Value)

	if replay.CheckAndMark(thash) {
		log.Printf("Reject: lnurlw code already used (hash=%s, source=%s)", thash[:16], cred.Source)
		replyMessage("Rejected: LNURLw code already used")
		os.Exit(1)
	}

	// TODO: Actually claim the LNURL-w payment here.
	// For now: accept any lnurlw string as valid payment.
	// This is a technology demonstration — not production.

	log.Printf("Accept (TODO): session=%s type=lnurlw hash=%s source=%s lnurlw=%s — pass-through accept, no payment claimed",
		sessionID, thash[:16], cred.Source, redact.LogSafe(redact.Truncate(cred.Value, 80)))

	recordLedgerAuth(ledger.LedgerEntry{
		EventType:   ledger.EventAuthAccept,
		MAC:         sessionID,
		PaymentType: "lnurlw",
		AmountSat:   LNURLWDefaultSec / RateSecPerSat,
		DurationSec: LNURLWDefaultSec,
		TokenHash:   thash,
		ReplyMessage: fmt.Sprintf("Valid LNURLw code: %dm access (TODO: claim Lightning payment)", LNURLWDefaultSec/60),
	})

	classStr := emitClass(operatorID, sessionID, thash[:16], opResolver.HMACKey())
	rec := &SessionRecord{
		MAC:      sessionID,
		Token:    thash,
		Guest:    "radius-lnurlw-" + thash[:8],
		Mint:     "lnurlw-pending",
		Amount:   LNURLWDefaultSec / RateSecPerSat,
		Started:  time.Now(),
		Duration: LNURLWDefaultSec,
		Source:   cred.Source,
		PayType:  radiusauth.PaymentLNURLW,
		Class:    classStr,
	}
	if err := sessions.Save(rec); err != nil {
		log.Printf("Warning: failed to save session record: %v", err)
	}

	replyMessage("Valid LNURLw code: %dm access (TODO: claim Lightning payment)", LNURLWDefaultSec/60)
	radiusAttr("Session-Timeout", LNURLWDefaultSec)
	radiusAttr("Acct-Interim-Interval", 60)
	os.Exit(0)
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

func handleCashuDelegated(cred radiusauth.PaymentCredential, sessionID string, sessions *SessionStore, operatorID string) {
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

	// Top-up (RFC 5176 CoA): extend NAS Session-Timeout without disconnect
	if existingRec, hasExisting := sessions.Get(sessionID); hasExisting && sessions.IsActive(existingRec) {
		if nasIP := getEnv("TOLLGATE_NAS_IP", ""); nasIP != "" {
			log.Printf("CoA: top-up detected for session=%s, sending CoA to nas=%s", sessionID, nasIP)
			sendCoAOrDisconnect(nasIP, seconds, "", sessionID)
		}
	}

	thash := cashu.TokenHash(cred.Value)

	classStr := emitClass(operatorID, sessionID, thash[:16], opResolver.HMACKey())
	rec := &SessionRecord{
		MAC:      sessionID,
		Token:    thash,
		Guest:    "radius-delegated-" + thash[:8],
		Mint:     "delegated",
		Amount:   minutes,
		Started:  time.Now(),
		Duration: seconds,
		Source:   cred.Source,
		PayType:  radiusauth.PaymentCashu,
		Class:    classStr,
	}
	if err := sessions.Save(rec); err != nil {
		log.Printf("Warning: failed to save session record: %v", err)
	}

	log.Printf("Accept: session=%s type=delegated duration=%ds source=%s",
		sessionID, seconds, cred.Source)

	recordLedgerAuth(ledger.LedgerEntry{
		EventType:   ledger.EventAuthAccept,
		MAC:         sessionID,
		PaymentType: "delegated",
		AmountSat:   minutes,
		DurationSec: seconds,
		TokenHash:   thash,
		ReplyMessage: fmt.Sprintf("Valid Cashu token: %dm access (delegated)", minutes),
	})

	replyMessage("Valid Cashu token: %dm access (delegated)", minutes)
	radiusAttr("Session-Timeout", seconds)
	radiusAttr("Acct-Interim-Interval", 60)
	os.Exit(0)
}
