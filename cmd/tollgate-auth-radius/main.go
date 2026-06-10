package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"tollgate-auth/internal/cashu"
)

// --- Configuration ---

const (
	BaseDir          = "/opt/cashu-tollgate"
	WalletDir        = "/var/lib/cashu-wallet"
	TokensLogFile    = BaseDir + "/radius-tokens.log"
	RateSecPerSat    = 60
	LNURLWDefaultSec = 3600 // 1 hour default for lnurlw (until we claim and know the amount)
	MaxInputLen      = 8192 // Maximum length for username/password fields
)

// macPattern validates Calling-Station-Id: hex digits, colons, dashes, or empty.
var macPattern = regexp.MustCompile(`^[0-9a-fA-F:\-]*$`)

// testMintPattern matches mint URLs that are allowed for token redemption.
// Only mints with "test" in the URL (case-insensitive) are accepted.
// This prevents accidental redemption of tokens with real monetary value.
// Customize this regex to allow additional mints.
var testMintPattern = regexp.MustCompile(`(?i)test`)

// PaymentType identifies the kind of payment credential.
type PaymentType string

const (
	PaymentCashu  PaymentType = "cashu"
	PaymentLNURLW PaymentType = "lnurlw"
)

// PaymentCredential holds an extracted payment string and its type.
type PaymentCredential struct {
	Value  string
	Source string // "username" or "password"
	Type   PaymentType
}

// SessionStore tracks active RADIUS sessions by MAC address.
// When a phone reconnects (sleep/wake), we skip token validation
// if the session is still within its paid time window.
type SessionStore struct {
	Dir string
}

type SessionRecord struct {
	MAC      string      `json:"mac"`
	Token    string      `json:"token_hash"`
	Guest    string      `json:"guest"`
	Mint     string      `json:"mint"`
	Amount   int         `json:"amount"`
	Started  time.Time   `json:"started"`
	Duration int         `json:"duration"` // seconds
	Source   string      `json:"source"`   // "username" or "password"
	PayType  PaymentType `json:"pay_type"` // "cashu" or "lnurlw"
}

// sanitizeMAC strips separators and validates format.
// Returns the clean hex-only MAC and whether it's valid.
// Rejects path traversal attempts (e.g. "../").
func sanitizeMAC(mac string) (string, bool) {
	if !macPattern.MatchString(mac) {
		return "", false
	}
	clean := strings.ToLower(mac)
	clean = strings.ReplaceAll(clean, ":", "")
	clean = strings.ReplaceAll(clean, "-", "")
	return clean, true
}

func (s *SessionStore) Path(mac string) string {
	clean, ok := sanitizeMAC(mac)
	if !ok {
		// Fallback: hash the input to prevent path traversal
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

// isCashuToken checks if a string looks like a Cashu token (cashuA or cashuB prefix).
func isCashuToken(s string) bool {
	return strings.HasPrefix(s, "cashuA") || strings.HasPrefix(s, "cashuB")
}

// isLNURLw checks if a string looks like an LNURL-withdraw code.
// LNURLw is bech32 encoded with HRP "lnurlw" (case-insensitive, all same case).
func isLNURLw(s string) bool {
	if len(s) < 10 {
		return false
	}
	lower := strings.ToLower(s)
	return strings.HasPrefix(lower, "lnurlw")
}

// base64urlPattern matches strings that contain only base64url characters.
// Used to detect the tail portion of a split Cashu token in the username field.
var base64urlPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func isBase64url(s string) bool {
	return len(s) > 0 && base64urlPattern.MatchString(s)
}

// sanitizeInput rejects strings containing characters that could break
// FreeRADIUS exec argument splitting or cause other issues.
// Rejects: single quotes, backslashes, newlines, null bytes.
// Also rejects strings exceeding MaxInputLen.
func sanitizeInput(s string) bool {
	if len(s) > MaxInputLen {
		return false
	}
	return !strings.ContainsAny(s, "'\\\n\r\x00")
}

// Split token threshold: Cashu tokens are 378 bytes, RADIUS attributes max 253.
// We split at 200 bytes — first part goes in password (starts with cashuB prefix),
// second part goes in username (base64url tail). Both under 253 bytes.
// No-DLEQ tokens are ~230 bytes and fit in a single field — no split needed.
const tokenSplitLen = 200

// extractPayment finds a payment credential (Cashu token or LNURLw) in username or password.
//
// Detection order:
// 1. Full token in username (cashuA/B prefix in username field)
// 2. LNURLw in any field
// 3. Split token (password <= tokenSplitLen starts with cashuB, username is base64url tail)
// 4. Full token in password/cleartext-password (cashuA/B prefix, must be > tokenSplitLen)
//
// The length guard on step 3 prevents misinterpreting a valid 230-byte no-DLEQ token
// (which starts with "cashuB" just like a split password) as a split. Split passwords
// are always exactly tokenSplitLen bytes by construction in the mint script.
func extractPayment(username, password, clearTextPw string) (PaymentCredential, bool) {
	// Full token in username
	if isCashuToken(username) && sanitizeInput(username) {
		return PaymentCredential{Value: username, Source: "username", Type: PaymentCashu}, true
	}
	if isLNURLw(username) && sanitizeInput(username) {
		return PaymentCredential{Value: username, Source: "username", Type: PaymentLNURLW}, true
	}
	if isLNURLw(password) && sanitizeInput(password) {
		return PaymentCredential{Value: password, Source: "password", Type: PaymentLNURLW}, true
	}
	if isLNURLw(clearTextPw) && sanitizeInput(clearTextPw) {
		return PaymentCredential{Value: clearTextPw, Source: "cleartext-password", Type: PaymentLNURLW}, true
	}
	// Split token: password is the first tokenSplitLen bytes of a 378-byte DLEQ token.
	// Only triggers when password starts with cashuB AND is exactly tokenSplitLen bytes
	// (the length our mint script produces) AND username is base64url-only.
	if isCashuToken(password) && len(password) <= tokenSplitLen &&
		isBase64url(username) && !isCashuToken(username) && !isLNURLw(username) &&
		sanitizeInput(password) && sanitizeInput(username) {
		fullToken := password + username
		if sanitizeInput(fullToken) {
			return PaymentCredential{Value: fullToken, Source: "split-password+username", Type: PaymentCashu}, true
		}
	}
	if isCashuToken(clearTextPw) && len(clearTextPw) <= tokenSplitLen &&
		isBase64url(username) && !isCashuToken(username) && !isLNURLw(username) &&
		sanitizeInput(clearTextPw) && sanitizeInput(username) {
		fullToken := clearTextPw + username
		if sanitizeInput(fullToken) {
			return PaymentCredential{Value: fullToken, Source: "split-cleartext+username", Type: PaymentCashu}, true
		}
	}
	// Full token in password (no-DLEQ 230b or any complete token > tokenSplitLen)
	if isCashuToken(password) && sanitizeInput(password) {
		return PaymentCredential{Value: password, Source: "password", Type: PaymentCashu}, true
	}
	if isCashuToken(clearTextPw) && sanitizeInput(clearTextPw) {
		return PaymentCredential{Value: clearTextPw, Source: "cleartext-password", Type: PaymentCashu}, true
	}
	return PaymentCredential{}, false
}

// safeLog sanitizes a string for log output by stripping newlines.
func safeLog(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	return s
}

// replyMessage outputs a Reply-Message attribute to stdout.
// FreeRADIUS exec module with output=reply parses stdout as RADIUS attribute pairs.
// Format: Reply-Message = "value"
func replyMessage(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	// Sanitize the message — no newlines or quotes that would break RADIUS parsing
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", "")
	msg = strings.ReplaceAll(msg, `"`, `'`)
	fmt.Printf("Reply-Message = \"%s\"\n", msg)
}

// isTestMint checks if a mint URL matches the allowed test mint pattern.
func isTestMint(mintURL string) bool {
	return testMintPattern.MatchString(mintURL)
}

// --- Main ---

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

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

	// Validate MAC format (prevents path traversal)
	if !macPattern.MatchString(mac) {
		log.Printf("Reject: invalid MAC format: %q", safeLog(truncate(mac, 32)))
		replyMessage("Rejected: invalid session identifier")
		os.Exit(1)
	}

	sessions := &SessionStore{Dir: BaseDir + "/radius-sessions"}
	os.MkdirAll(sessions.Dir, 0700)

	replay := cashu.NewReplayGuard(BaseDir + "/radius-spent.txt")
	os.MkdirAll(BaseDir, 0755)

	// --- Check for existing active session (reconnection) ---
	if rec, found := sessions.Get(mac); found {
		if sessions.IsActive(rec) {
			remaining := time.Until(rec.Started.Add(time.Duration(rec.Duration) * time.Second))
			log.Printf("Reconnection: MAC=%s session active (%dm remaining), accepting", mac, int(remaining.Minutes()))
			replyMessage("Session resumed: %dm remaining (type=%s, amount=%d sat)",
				int(remaining.Minutes()), rec.PayType, rec.Amount)
			os.Exit(0)
		}
		log.Printf("Reconnection: MAC=%s session expired, removing", mac)
		sessions.Remove(mac)
	}

	// --- Extract payment credential from username or password ---
	cred, found := extractPayment(username, password, clearTextPw)
	if !found {
		log.Printf("Reject: no payment credential found in username or password (user=%q)", safeLog(truncate(username, 32)))
		replyMessage("Rejected: no valid payment credential found")
		os.Exit(1)
	}

	// --- Route by payment type ---
	switch cred.Type {
	case PaymentCashu:
		handleCashu(cred, mac, sessions, replay)
	case PaymentLNURLW:
		handleLNURLw(cred, mac, sessions, replay)
	}
}

// handleCashu validates a Cashu token: decode → verify → redeem → session.
// Only redeems tokens from test mints (matching testMintPattern regex).
func handleCashu(cred PaymentCredential, mac string, sessions *SessionStore, replay *cashu.ReplayGuard) {
	tokenData, err := cashu.DecodeToken(cred.Value)
	if err != nil {
		log.Printf("Reject: cashu decode failed (%s): %v", cred.Source, err)
		replyMessage("Rejected: invalid Cashu token format")
		cashu.LogToken(cred.Value, &cashu.TokenData{}, "radius-"+mac, false, TokensLogFile)
		os.Exit(1)
	}

	thash := cashu.TokenHash(cred.Value)
	seconds := tokenData.Amount * RateSecPerSat
	minutes := seconds / 60

	// Replay check
	if replay.IsSpent(thash) {
		log.Printf("Reject: cashu token already spent (hash=%s, source=%s)", thash[:16], cred.Source)
		replyMessage("Rejected: token already used")
		os.Exit(1)
	}

	// Mint allowlist: only accept test mints
	if !isTestMint(tokenData.Mint) {
		log.Printf("Reject: non-test mint (%s) — only test mints are accepted", tokenData.Mint)
		replyMessage("Rejected: mint '%s' is not a testnet mint — only test mints accepted", tokenData.Mint)
		cashu.LogToken(cred.Value, tokenData, "radius-"+mac, false, TokensLogFile)
		os.Exit(1)
	}

	ok, msg := cashu.VerifyWithMint(tokenData)
	if !ok {
		log.Printf("Reject: cashu mint verification failed (%s): %s", cred.Source, msg)
		replyMessage("Rejected: mint verification failed — %s", msg)
		cashu.LogTokenWithError(cred.Value, tokenData, "radius-"+mac, false, "verify: "+msg, TokensLogFile)
		os.Exit(1)
	}

	if err := cashu.RedeemToken(cred.Value, WalletDir); err != nil {
		log.Printf("Reject: cashu redemption failed (%s): %v", cred.Source, err)
		replyMessage("Rejected: token redemption failed")
		cashu.LogTokenWithError(cred.Value, tokenData, "radius-"+mac, false, "redeem: "+err.Error(), TokensLogFile)
		os.Exit(1)
	}

	// Mark spent & log
	replay.MarkSpent(thash)
	cashu.LogToken(cred.Value, tokenData, "radius-"+mac, true, TokensLogFile)

	// Save session
	rec := &SessionRecord{
		MAC:      mac,
		Token:    thash,
		Guest:    "radius-" + thash[:8],
		Mint:     tokenData.Mint,
		Amount:   tokenData.Amount,
		Started:  time.Now(),
		Duration: seconds,
		Source:   cred.Source,
		PayType:  PaymentCashu,
	}
	if err := sessions.Save(rec); err != nil {
		log.Printf("Warning: failed to save session record: %v", err)
	}

	log.Printf("Accept: MAC=%s type=cashu amount=%d sat duration=%ds mint=%s source=%s",
		mac, tokenData.Amount, seconds, tokenData.Mint, cred.Source)

	replyMessage("Valid Cashu token: %d sat = %dm access from %s",
		tokenData.Amount, minutes, tokenData.Mint)
	os.Exit(0)
}

// handleLNURLw processes an LNURL-withdraw code.
//
// TODO: Implement actual LNURL-w claiming:
//   1. Decode bech32 (HRP=lnurlw) → get callback URL
//   2. GET callback URL → receive {callback, k1, maxWithdrawable, ...}
//   3. Generate a Lightning invoice for maxWithdrawable (or a fixed amount)
//   4. GET callback?k1=...&pr=<bolt11_invoice> → claim the payment
//   5. Wait for invoice settlement → determine amount → set session duration
//
// For now: pass-through accept with default duration (technology demo / PoC).
func handleLNURLw(cred PaymentCredential, mac string, sessions *SessionStore, replay *cashu.ReplayGuard) {
	// Basic replay protection using hash of the lnurlw string
	thash := cashu.TokenHash(cred.Value)

	if replay.IsSpent(thash) {
		log.Printf("Reject: lnurlw code already used (hash=%s, source=%s)", thash[:16], cred.Source)
		replyMessage("Rejected: LNURLw code already used")
		os.Exit(1)
	}

	// TODO: Actually claim the LNURL-w payment here.
	// For now: accept any lnurlw string as valid payment.
	// This is a technology demonstration — not production.

	replay.MarkSpent(thash)
	log.Printf("Accept (TODO): MAC=%s type=lnurlw hash=%s source=%s lnurlw=%s — pass-through accept, no payment claimed",
		mac, thash[:16], cred.Source, safeLog(truncate(cred.Value, 80)))

	// Save session with default duration
	rec := &SessionRecord{
		MAC:      mac,
		Token:    thash,
		Guest:    "radius-lnurlw-" + thash[:8],
		Mint:     "lnurlw-pending",
		Amount:   LNURLWDefaultSec / RateSecPerSat, // implied sats
		Started:  time.Now(),
		Duration: LNURLWDefaultSec,
		Source:   cred.Source,
		PayType:  PaymentLNURLW,
	}
	if err := sessions.Save(rec); err != nil {
		log.Printf("Warning: failed to save session record: %v", err)
	}

	replyMessage("Valid LNURLw code: %dm access (TODO: claim Lightning payment)", LNURLWDefaultSec/60)
	os.Exit(0)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
