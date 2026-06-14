package main

import (
	"fmt"
	"regexp"
	"time"

	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/fakeverity"
	"tollgate-auth/internal/radius"
	"tollgate-auth/internal/radiusauth"
	"tollgate-auth/internal/redact"
)

const (
	authRateSecPerSat    = 60
	authLNURLWDefaultSec = 3600
)

var authTestMintPattern = regexp.MustCompile(`(?i)test`)
var authMacPattern = regexp.MustCompile(`^[0-9a-fA-F:\-\.]*$`)

// AuthResult holds the outcome of processing a RADIUS auth request.
type AuthResult struct {
	Accept         bool
	ReplyMessage   string
	SessionTimeout int
	AcctInterval   int
	Class          string
	LogMessage     string
}

// Dependencies holds all injectable dependencies for auth processing.
type Dependencies struct {
	Sessions    *SessionStore
	Replay      fakeverity.ReplayGuard
	Verifier    fakeverity.Verifier
	OperatorID  string
	HMACKey     []byte
	AuthMode    string
	SessiondURL string
}

func isTestMintCheck(mintURL string) bool {
	return authTestMintPattern.MatchString(mintURL)
}

// processAuth contains all auth logic extracted from main().
// Returns AuthResult instead of calling os.Exit or printing to stdout.
func processAuth(deps *Dependencies, username, mac, password, clearTextPw string) AuthResult {
	if !authMacPattern.MatchString(mac) {
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
					ReplyMessage:   fmt.Sprintf("Session resumed: %dm remaining (type=%s, amount=%d sat)", int(remaining.Minutes()), rec.PayType, rec.Amount),
					SessionTimeout: max(1, int(remaining.Seconds())),
					AcctInterval:   60,
					Class:          rec.Class,
					LogMessage:     fmt.Sprintf("Reconnection: session=%s active (%dm remaining), accepting", sessionID, int(remaining.Minutes())),
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
	seconds := tokenData.Amount * authRateSecPerSat
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
				LogMessage:   fmt.Sprintf("Reject: token %s in spent-hashes but UNSPENT at mint — possible replay", thash[:16]),
			}

		default:
			return AuthResult{
				Accept:       false,
				ReplyMessage: fmt.Sprintf("Rejected: cannot verify token state — %s", stateMsg),
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

	if !isTestMintCheck(tokenData.Mint) {
		return AuthResult{
			Accept:       false,
			ReplyMessage: fmt.Sprintf("Rejected: mint '%s' is not a testnet mint — only test mints accepted", tokenData.Mint),
			LogMessage:   fmt.Sprintf("Reject: non-test mint (%s) — only test mints are accepted", tokenData.Mint),
		}
	}

	ok, msg := deps.Verifier.Verify(tokenData)
	if !ok {
		return AuthResult{
			Accept:       false,
			ReplyMessage: fmt.Sprintf("Rejected: mint verification failed — %s", msg),
			LogMessage:   fmt.Sprintf("Reject: cashu mint verification failed (%s): %s", cred.Source, msg),
		}
	}

	if err := deps.Verifier.Redeem(cred.Value); err != nil {
		return AuthResult{
			Accept:       false,
			ReplyMessage: "Rejected: token redemption failed",
			LogMessage:   fmt.Sprintf("Reject: cashu redemption failed (%s): %v", cred.Source, err),
		}
	}

	classStr := emitClassForTest(deps.OperatorID, sessionID, thash[:16], deps.HMACKey)
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
	deps.Sessions.Save(rec)

	return AuthResult{
		Accept: true,
		ReplyMessage: fmt.Sprintf("Valid Cashu token: %d sat = %dm access from %s",
			tokenData.Amount, minutes, tokenData.Mint),
		SessionTimeout: seconds,
		AcctInterval:   60,
		Class:          classStr,
		LogMessage: fmt.Sprintf("Accept: session=%s type=cashu amount=%d sat duration=%ds mint=%s source=%s",
			sessionID, tokenData.Amount, seconds, tokenData.Mint, cred.Source),
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

	classStr := emitClassForTest(deps.OperatorID, sessionID, thash[:16], deps.HMACKey)
	rec := &SessionRecord{
		MAC:      sessionID,
		Token:    thash,
		Guest:    "radius-lnurlw-" + thash[:8],
		Mint:     "lnurlw-pending",
		Amount:   authLNURLWDefaultSec / authRateSecPerSat,
		Started:  time.Now(),
		Duration: authLNURLWDefaultSec,
		Source:   cred.Source,
		PayType:  radiusauth.PaymentLNURLW,
		Class:    classStr,
	}
	deps.Sessions.Save(rec)

	return AuthResult{
		Accept:         true,
		ReplyMessage:   fmt.Sprintf("Valid LNURLw code: %dm access (TODO: claim Lightning payment)", authLNURLWDefaultSec/60),
		SessionTimeout: authLNURLWDefaultSec,
		AcctInterval:   60,
		Class:          classStr,
		LogMessage: fmt.Sprintf("Accept (TODO): session=%s type=lnurlw hash=%s source=%s — pass-through accept",
			sessionID, thash[:16], cred.Source),
	}
}

func processCashuDelegated(deps *Dependencies, cred radiusauth.PaymentCredential, sessionID string) AuthResult {
	return AuthResult{
		Accept:       false,
		ReplyMessage: "Rejected: delegated mode requires sessiond client (not available in processAuth)",
		LogMessage:   "Reject: delegated mode not supported in processAuth — use handleCashuDelegated in main.go",
	}
}

// emitClassForTest creates an HMAC-signed Class attribute without printing to stdout.
func emitClassForTest(operatorID, sessionID, tokenHash string, hmacKey []byte) string {
	sc := radius.NewSessionClass(operatorID, sessionID, tokenHash)
	signed, err := sc.HMACSign(hmacKey)
	if err != nil {
		return ""
	}
	return signed
}
