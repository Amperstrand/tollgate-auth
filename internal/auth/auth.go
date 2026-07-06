// Package auth contains the shared authentication pipeline used by both
// the FreeRADIUS exec binary (cmd/tollgate-auth-radius) and the persistent
// daemon (cmd/tollgate-daemon).
//
// The pipeline is transport-agnostic: callers populate a Dependencies struct,
// call ProcessAuth, and receive an AuthResult. No os.Exit, no stdout output.
package auth

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/fakeverity"
	"tollgate-auth/internal/ledger"
	"tollgate-auth/internal/radius"
	"tollgate-auth/internal/radiusauth"
	"tollgate-auth/internal/redact"
)

// Rate constants.
const (
	RateSecPerSat    = 10 // RADIUS-only: legacy seconds-per-sat for captive portal timeout. OCPI charger computes time from kWh.
	LNURLWDefaultSec = 600
)

// Patterns.
var (
	TestMintPattern = regexp.MustCompile(`.*`)
	MacPattern      = regexp.MustCompile(`^[0-9a-fA-F:\-\.]*$`)
)

// AuthResult holds the outcome of processing a RADIUS auth request.
type AuthResult struct {
	Accept         bool
	ReplyMessage   string
	SessionTimeout int
	AcctInterval   int
	Class          string
	LogMessage     string
	CreditAmount   int
	Unit           string
	MintURL        string
	TokenHash      string
	PayType        string
}

// Dependencies holds all injectable dependencies for auth processing.
type Dependencies struct {
	Sessions     *SessionStore
	Replay       fakeverity.ReplayGuard
	Verifier     fakeverity.Verifier
	Bootstrapper fakeverity.Bootstrapper
	OperatorID   string
	HMACKey      []byte
	AuthMode     string // "local" or "delegated"
	SessiondURL  string
	Ledger       *ledger.Ledger
}

// SessionRecord tracks a single RADIUS session.
type SessionRecord struct {
	MAC      string                 `json:"mac"`
	Token    string                 `json:"token_hash"`
	Guest    string                 `json:"guest"`
	Mint     string                 `json:"mint"`
	Amount   int                    `json:"amount"`
	Unit     string                 `json:"unit"`
	Started  time.Time              `json:"started"`
	Duration int                    `json:"duration"` // seconds
	Source   string                 `json:"source"`   // "username" or "password"
	PayType  radiusauth.PaymentType `json:"pay_type"` // "cashu" or "lnurlw"
	Class    string                 `json:"class"`
}

// SessionStore tracks active sessions by MAC address.
// When a phone reconnects (sleep/wake), we skip token validation
// if the session is still within its paid time window.
// Safe for concurrent use.
type SessionStore struct {
	Dir string
	mu  sync.RWMutex
}

func (s *SessionStore) Path(mac string) string {
	clean, ok := radiusauth.SanitizeMAC(mac)
	if !ok {
		clean = cashu.TokenHash(mac)[:16]
	}
	return filepath.Join(s.Dir, clean+".json")
}

func (s *SessionStore) Get(mac string) (*SessionRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

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
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path(rec.MAC), data, 0600)
}

func (s *SessionStore) Remove(mac string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	os.Remove(s.Path(mac))
}

// Cleanup removes expired session files. Called probabilistically to avoid
// overhead on every invocation (~5% of requests trigger cleanup).
func (s *SessionStore) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.Dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rec SessionRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			os.Remove(path)
			continue
		}
		deadline := rec.Started.Add(time.Duration(rec.Duration) * time.Second)
		if now.After(deadline.Add(time.Hour)) {
			os.Remove(path)
		}
	}
}

// IsTestMint checks if a mint URL matches the allowed test mint pattern.
func IsTestMint(mintURL string) bool {
	return TestMintPattern.MatchString(mintURL)
}

// EmitClass creates an HMAC-signed Class attribute string.
// The Class attribute ties the session to the operator for accounting (RFC 2865 §5.5).
func EmitClass(operatorID, sessionID, tokenHash string, hmacKey []byte) string {
	sc := radius.NewSessionClass(operatorID, sessionID, tokenHash)
	signed, err := sc.HMACSign(hmacKey)
	if err != nil {
		return ""
	}
	return signed
}

func sanitizeLogField(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return "[contains control chars]"
		}
	}
	return s
}

// ProcessAuth contains all auth logic.
// Returns AuthResult instead of calling os.Exit or printing to stdout.
// This is the unified entry point used by both the exec binary and the daemon.
func ProcessAuth(deps *Dependencies, username, mac, password, clearTextPw, nasID, clientIP string) AuthResult {
	if strings.HasPrefix(nasID, "npub1") && len(nasID) == 63 {
		log.Printf("Operator AP npub from NAS-Identifier: %s", sanitizeLogField(nasID))
	}

	if !MacPattern.MatchString(mac) {
		return AuthResult{
			Accept:       false,
			ReplyMessage: "Rejected: invalid session identifier",
			LogMessage:   fmt.Sprintf("Reject: invalid MAC format: %q", redact.LogSafe(redact.Truncate(mac, 32))),
		}
	}

	sessionID := mac
	if sessionID == "" {
		sessionID = "user:" + username
	}

	if deps.AuthMode != "delegated" {
		if rec, found := deps.Sessions.Get(sessionID); found {
			if deps.Sessions.IsActive(rec) {
				remaining := time.Until(rec.Started.Add(time.Duration(rec.Duration) * time.Second))
				return AuthResult{
					Accept:         true,
					ReplyMessage:   fmt.Sprintf("Session resumed: %dm remaining (type=%s, amount=%d %s)", int(remaining.Minutes()), rec.PayType, rec.Amount, rec.Unit),
					SessionTimeout: max(1, int(remaining.Seconds())),
					AcctInterval:   60,
					Class:          rec.Class,
					LogMessage:     fmt.Sprintf("Reconnection: session=%s active (%dm remaining), accepting", sessionID, int(remaining.Minutes())),
					CreditAmount:   rec.Amount,
					Unit:           rec.Unit,
					MintURL:        rec.Mint,
					TokenHash:      rec.Token,
					PayType:        string(rec.PayType),
				}
			}
			deps.Sessions.Remove(sessionID)
		}
	}

	cred, found := radiusauth.ExtractPayment(username, password, clearTextPw)

	if deps.AuthMode == "delegated" && !found {
		return AuthResult{
			Accept:       false,
			ReplyMessage: "Rejected: no valid payment credential found",
			LogMessage:   fmt.Sprintf("Reject: no payment credential and no active session (delegated, mac=%s)", sessionID),
		}
	}

	if !found {
		return AuthResult{
			Accept:       false,
			ReplyMessage: "Rejected: no valid payment credential found",
			LogMessage:   fmt.Sprintf("Reject: no payment credential found in username or password (user=%q)", redact.LogSafe(redact.Truncate(username, 32))),
		}
	}

	switch cred.Type {
	case radiusauth.PaymentCashu:
		if deps.AuthMode == "delegated" {
			return processCashuDelegated(deps, cred, sessionID)
		}
		return processCashu(deps, cred, sessionID)
	case radiusauth.PaymentLNURLW:
		return processLNURLw(deps, cred, sessionID)
	}

	return AuthResult{
		Accept:       false,
		ReplyMessage: "Rejected: unknown payment type",
		LogMessage:   "Reject: unknown payment type",
	}
}

func processCashu(deps *Dependencies, cred radiusauth.PaymentCredential, sessionID string) AuthResult {
	tokenData, err := deps.Verifier.Decode(cred.Value)
	if err != nil {
		return AuthResult{
			Accept:       false,
			ReplyMessage: "Rejected: invalid Cashu token format",
			LogMessage:   fmt.Sprintf("Reject: cashu decode failed (%s): %v", cred.Source, err),
		}
	}

	thash := cashu.TokenHash(cred.Value)
	seconds := tokenData.Amount * RateSecPerSat
	minutes := seconds / 60

	if tokenData.Amount <= 0 {
		return AuthResult{
			Accept:       false,
			ReplyMessage: "Rejected: token has zero value",
			LogMessage:   fmt.Sprintf("Reject: zero or negative amount (%d) in token", tokenData.Amount),
		}
	}

	// --- Idempotent redemption state machine ---
	alreadyMarked := deps.Replay.IsSpent(thash)

	if alreadyMarked {
		state, stateMsg := deps.Verifier.CheckState(tokenData)

		switch state {
		case cashu.StateSpent:
			if rec, found := deps.Sessions.Get(sessionID); found && deps.Sessions.IsActive(rec) {
				remaining := time.Until(rec.Started.Add(time.Duration(rec.Duration) * time.Second))
				return AuthResult{
					Accept:         true,
					ReplyMessage:   fmt.Sprintf("Session resumed: %dm remaining", int(remaining.Minutes())),
					SessionTimeout: max(1, int(remaining.Seconds())),
					AcctInterval:   60,
					Class:          rec.Class,
					LogMessage:     fmt.Sprintf("Reconnect: session=%s recovered (spent token, %ds remaining)", sessionID, max(1, int(remaining.Seconds()))),
					CreditAmount:   rec.Amount,
					Unit:           rec.Unit,
					MintURL:        rec.Mint,
					TokenHash:      rec.Token,
					PayType:        string(rec.PayType),
				}
			}

			return AuthResult{
				Accept:       false,
				ReplyMessage: "Rejected: token already spent",
				LogMessage:   fmt.Sprintf("Reject: token %s spent at mint, no active session for %s", thash[:16], sessionID),
			}

		case cashu.StatePending:
			return AuthResult{
				Accept:       false,
				ReplyMessage: "Rejected: token is being processed, please try again in a few seconds",
				LogMessage:   fmt.Sprintf("Reject: token %s pending at mint (hash=%s)", thash[:16], thash[:16]),
			}

		case cashu.StateUnspent:
			return AuthResult{
				Accept:       false,
				ReplyMessage: "Rejected: token already used",
				LogMessage:   fmt.Sprintf("Reject: token %s in spent-hashes but UNSPENT at mint \u2014 possible replay", thash[:16]),
			}

		default:
			return AuthResult{
				Accept:       false,
				ReplyMessage: fmt.Sprintf("Rejected: cannot verify token state \u2014 %s", stateMsg),
				LogMessage:   fmt.Sprintf("Reject: cannot determine token state during recovery (%s): %s", thash[:16], stateMsg),
			}
		}
	}

	if !alreadyMarked {
		if deps.Replay.CheckAndMark(thash) {
			return AuthResult{
				Accept:       false,
				ReplyMessage: "Rejected: token already used",
				LogMessage:   fmt.Sprintf("Reject: cashu token race (hash=%s, source=%s)", thash[:16], cred.Source),
			}
		}
	}

	if !IsTestMint(tokenData.Mint) {
		return AuthResult{
			Accept:       false,
			ReplyMessage: fmt.Sprintf("Rejected: mint '%s' is not allowed", tokenData.Mint),
			LogMessage:   fmt.Sprintf("Reject: mint not allowed (%s)", tokenData.Mint),
		}
	}

	ok, msg := deps.Verifier.Verify(tokenData)
	if !ok {
		return AuthResult{
			Accept:       false,
			ReplyMessage: fmt.Sprintf("Rejected: mint verification failed \u2014 %s", msg),
			LogMessage:   fmt.Sprintf("Reject: cashu mint verification failed (%s): %s", cred.Source, msg),
		}
	}

	if err := deps.Verifier.Redeem(cred.Value); err != nil {
		log.Printf("WARN: cashu redemption failed (%s), accepting with replay guard only: %v", cred.Source, err)
	}

	classStr := EmitClass(deps.OperatorID, sessionID, thash[:32], deps.HMACKey)
	rec := &SessionRecord{
		MAC:      sessionID,
		Token:    thash,
		Guest:    "radius-" + thash[:16],
		Mint:     tokenData.Mint,
		Amount:   tokenData.Amount,
		Unit:     tokenData.Unit,
		Started:  time.Now(),
		Duration: seconds,
		Source:   cred.Source,
		PayType:  radiusauth.PaymentCashu,
		Class:    classStr,
	}
	deps.Sessions.Save(rec)

	return AuthResult{
		Accept: true,
		ReplyMessage: fmt.Sprintf("Valid Cashu token: %d %s = %dm access from %s",
			tokenData.Amount, tokenData.Unit, minutes, tokenData.Mint),
		SessionTimeout: seconds,
		AcctInterval:   60,
		Class:          classStr,
		LogMessage: fmt.Sprintf("Accept: session=%s type=cashu amount=%d %s duration=%ds mint=%s source=%s",
			sessionID, tokenData.Amount, tokenData.Unit, seconds, tokenData.Mint, cred.Source),
		CreditAmount: tokenData.Amount,
		Unit:         tokenData.Unit,
		MintURL:      tokenData.Mint,
		TokenHash:    thash,
		PayType:      string(radiusauth.PaymentCashu),
	}
}

func processLNURLw(deps *Dependencies, cred radiusauth.PaymentCredential, sessionID string) AuthResult {
	thash := cashu.TokenHash(cred.Value)

	if deps.Replay.CheckAndMark(thash) {
		return AuthResult{
			Accept:       false,
			ReplyMessage: "Rejected: LNURLw code already used",
			LogMessage:   fmt.Sprintf("Reject: lnurlw code already used (hash=%s, source=%s)", thash[:16], cred.Source),
		}
	}

	classStr := EmitClass(deps.OperatorID, sessionID, thash[:32], deps.HMACKey)
	rec := &SessionRecord{
		MAC:      sessionID,
		Token:    thash,
		Guest:    "radius-lnurlw-" + thash[:16],
		Mint:     "lnurlw-pending",
		Amount:   LNURLWDefaultSec / RateSecPerSat,
		Started:  time.Now(),
		Duration: LNURLWDefaultSec,
		Source:   cred.Source,
		PayType:  radiusauth.PaymentLNURLW,
		Class:    classStr,
	}
	deps.Sessions.Save(rec)

	return AuthResult{
		Accept:         true,
		ReplyMessage:   fmt.Sprintf("Valid LNURLw code: %dm access (TODO: claim Lightning payment)", LNURLWDefaultSec/60),
		SessionTimeout: LNURLWDefaultSec,
		AcctInterval:   60,
		Class:          classStr,
		LogMessage: fmt.Sprintf("Accept (TODO): session=%s type=lnurlw hash=%s source=%s \u2014 pass-through accept",
			sessionID, thash[:16], cred.Source),
		CreditAmount: 0,
		MintURL:      "lnurlw-pending",
		TokenHash:    thash,
		PayType:      string(radiusauth.PaymentLNURLW),
	}
}

func processCashuDelegated(deps *Dependencies, cred radiusauth.PaymentCredential, sessionID string) AuthResult {
	tokenData, err := deps.Verifier.Decode(cred.Value)
	if err != nil {
		return AuthResult{
			Accept:       false,
			ReplyMessage: "Rejected: invalid Cashu token format",
			LogMessage:   fmt.Sprintf("Reject: cashu decode failed (delegated, %s): %v", cred.Source, err),
		}
	}

	thash := cashu.TokenHash(cred.Value)

	alreadyMarked := deps.Replay.IsSpent(thash)

	if alreadyMarked {
		state, stateMsg := deps.Verifier.CheckState(tokenData)

		switch state {
		case cashu.StateSpent:
			if rec, found := deps.Sessions.Get(sessionID); found && deps.Sessions.IsActive(rec) {
				remaining := time.Until(rec.Started.Add(time.Duration(rec.Duration) * time.Second))
				return AuthResult{
					Accept:         true,
					ReplyMessage:   fmt.Sprintf("Session resumed: %dm remaining (delegated)", int(remaining.Minutes())),
					SessionTimeout: max(1, int(remaining.Seconds())),
					AcctInterval:   60,
					Class:          rec.Class,
					LogMessage:     fmt.Sprintf("Reconnect: session=%s recovered (delegated, spent token, %ds remaining)", sessionID, max(1, int(remaining.Seconds()))),
					CreditAmount:   rec.Amount,
					Unit:           rec.Unit,
					MintURL:        rec.Mint,
					TokenHash:      rec.Token,
					PayType:        string(rec.PayType),
				}
			}
			return AuthResult{
				Accept:       false,
				ReplyMessage: "Rejected: token already spent",
				LogMessage:   fmt.Sprintf("Reject: token %s spent at mint, no active session for %s (delegated)", thash[:16], sessionID),
			}

		case cashu.StatePending:
			return AuthResult{
				Accept:       false,
				ReplyMessage: "Rejected: token is being processed, please try again in a few seconds",
				LogMessage:   fmt.Sprintf("Reject: token %s pending at mint (delegated)", thash[:16]),
			}

		case cashu.StateUnspent:
			return AuthResult{
				Accept:       false,
				ReplyMessage: "Rejected: token already used",
				LogMessage:   fmt.Sprintf("Reject: token %s in spent-hashes but UNSPENT at mint (delegated)", thash[:16]),
			}

		default:
			return AuthResult{
				Accept:       false,
				ReplyMessage: fmt.Sprintf("Rejected: cannot verify token state \u2014 %s", stateMsg),
				LogMessage:   fmt.Sprintf("Reject: cannot determine token state (delegated, %s): %s", thash[:16], stateMsg),
			}
		}
	}

	if !alreadyMarked {
		if deps.Replay.CheckAndMark(thash) {
			return AuthResult{
				Accept:       false,
				ReplyMessage: "Rejected: token already used",
				LogMessage:   fmt.Sprintf("Reject: cashu token race (delegated, hash=%s, source=%s)", thash[:16], cred.Source),
			}
		}
	}

	result, err := deps.Bootstrapper.Bootstrap(cred.Value, sessionID)
	if err != nil {
		return AuthResult{
			Accept:       false,
			ReplyMessage: "Rejected: delegated session failed",
			LogMessage:   fmt.Sprintf("Reject: delegated bootstrap failed (%s): %v", cred.Source, err),
		}
	}

	seconds := int(result.AllotmentMs / 1000)
	minutes := seconds / 60

	if seconds <= 0 {
		return AuthResult{
			Accept:       false,
			ReplyMessage: "Rejected: zero allotment from server",
			LogMessage:   fmt.Sprintf("Reject: delegated bootstrap returned zero allotment for session=%s", sessionID),
		}
	}

	classStr := EmitClass(deps.OperatorID, sessionID, thash[:32], deps.HMACKey)

	var displayAmount int
	if result.CreditAmount > 0 {
		displayAmount = int(result.CreditAmount)
	} else {
		displayAmount = minutes
	}

	rec := &SessionRecord{
		MAC:      sessionID,
		Token:    thash,
		Guest:    "radius-delegated-" + thash[:16],
		Mint:     "delegated",
		Amount:   displayAmount,
		Unit:     tokenData.Unit,
		Started:  time.Now(),
		Duration: seconds,
		Source:   cred.Source,
		PayType:  radiusauth.PaymentCashu,
		Class:    classStr,
	}
	deps.Sessions.Save(rec)

	var replyMsg string
	if result.CreditAmount > 0 && result.EffectiveRateSecPerSat > 0 {
		replyMsg = fmt.Sprintf("Valid Cashu token: %d sat = %dm access (%ds/sat)",
			result.CreditAmount, minutes, result.EffectiveRateSecPerSat)
	} else {
		replyMsg = fmt.Sprintf("Valid Cashu token: %dm access (delegated)", minutes)
	}

	return AuthResult{
		Accept:         true,
		ReplyMessage:   replyMsg,
		SessionTimeout: seconds,
		AcctInterval:   60,
		Class:          classStr,
		LogMessage: fmt.Sprintf("Accept: session=%s type=delegated duration=%ds sats=%d source=%s",
			sessionID, seconds, result.CreditAmount, cred.Source),
		CreditAmount: displayAmount,
		Unit:         tokenData.Unit,
		MintURL:      "delegated",
		TokenHash:    thash,
		PayType:      string(radiusauth.PaymentCashu),
	}
}
