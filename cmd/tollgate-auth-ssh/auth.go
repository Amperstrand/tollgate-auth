package main

import (
	"fmt"
	"net"
	"strings"

	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/fakeverity"
	"tollgate-auth/internal/sessiond"
)

// AuthDecision holds the result of processing an SSH login.
type AuthDecision struct {
	Accept    bool
	Error     string // user-facing error message if rejected
	LogMsg    string // server-side log message
	Guest     string // guest username (g-<hash8>)
	TokenData *cashu.TokenData
	Seconds   int    // session duration in seconds
	TokenHash string // SHA256 hash of the token string
	MOTD      string // rendered MOTD
}

// Bootstrapper abstracts sessiond token bootstrap for delegated mode.
// *sessiond.Client satisfies this interface in production.
type Bootstrapper interface {
	Bootstrap(token string, mac string) (*sessiond.SessionState, error)
}

// SSHDependencies holds all injectable dependencies for SSH auth processing.
type SSHDependencies struct {
	Replay       fakeverity.ReplayGuard
	Verifier     fakeverity.Verifier
	Bootstrapper Bootstrapper
	AuthMode     string // "local" or "delegated"
	SessiondURL  string
	WalletDir    string
	TokensLog    string
}

// processSSHAuth validates a Cashu token for SSH access.
// Returns AuthDecision with Accept=true + details, or Accept=false + error message.
func processSSHAuth(deps *SSHDependencies, username string, remoteAddr string) AuthDecision {
	tokenData, err := deps.Verifier.Decode(username)
	if err != nil {
		return AuthDecision{
			Accept: false,
			Error:  err.Error(),
			LogMsg: fmt.Sprintf("Reject: token decode failed: %v", err),
		}
	}

	thash := cashu.TokenHash(username)
	guest := guestUsername(username)

	if deps.AuthMode == "delegated" {
		return processSSHDelegated(deps, username, tokenData, thash, guest, remoteAddr)
	}
	return processSSHLlocal(deps, username, tokenData, thash, guest)
}

func processSSHLlocal(deps *SSHDependencies, username string, tokenData *cashu.TokenData, thash, guest string) AuthDecision {
	if tokenData.Amount <= 0 {
		return AuthDecision{
			Accept:    false,
			Error:     "token has zero value",
			TokenData: tokenData,
			TokenHash: thash,
			Guest:     guest,
			LogMsg:    fmt.Sprintf("Reject: zero or negative amount (%d) in token", tokenData.Amount),
		}
	}

	// CheckAndMark atomically prevents replay — if already spent, reject.
	if deps.Replay.CheckAndMark(thash) {
		return AuthDecision{
			Accept:    false,
			Error:     "token already used",
			TokenData: tokenData,
			TokenHash: thash,
			Guest:     guest,
			LogMsg:    fmt.Sprintf("Reject: token already spent (hash=%s)", safeHashPrefix(thash)),
		}
	}

	ok, msg := deps.Verifier.Verify(tokenData)
	if !ok {
		return AuthDecision{
			Accept:    false,
			Error:     msg,
			TokenData: tokenData,
			TokenHash: thash,
			Guest:     guest,
			LogMsg:    fmt.Sprintf("Reject: mint verification failed: %s", msg),
		}
	}

	if err := deps.Verifier.Redeem(username); err != nil {
		return AuthDecision{
			Accept:    false,
			Error:     "token redemption failed",
			TokenData: tokenData,
			TokenHash: thash,
			Guest:     guest,
			LogMsg:    fmt.Sprintf("Reject: token redemption failed: %v", err),
		}
	}

	seconds := tokenData.Amount * RateSecPerSat
	return AuthDecision{
		Accept:    true,
		Guest:     guest,
		TokenData: tokenData,
		Seconds:   seconds,
		TokenHash: thash,
		MOTD:      renderMOTD(tokenData, guest, seconds),
		LogMsg:    fmt.Sprintf("Accept: guest=%s duration=%ds amount=%d mint=%s", guest, seconds, tokenData.Amount, tokenData.Mint),
	}
}

func processSSHDelegated(deps *SSHDependencies, username string, tokenData *cashu.TokenData, thash, guest, remoteAddr string) AuthDecision {
	if deps.Replay.CheckAndMark(thash) {
		return AuthDecision{
			Accept:    false,
			Error:     "token already used",
			TokenData: tokenData,
			TokenHash: thash,
			Guest:     guest,
			LogMsg:    fmt.Sprintf("Reject: token already spent (hash=%s)", safeHashPrefix(thash)),
		}
	}

	mac := remoteAddr
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		mac = "ssh:" + host
	}

	state, err := deps.Bootstrapper.Bootstrap(username, mac)
	if err != nil {
		return AuthDecision{
			Accept:    false,
			Error:     fmt.Sprintf("delegated session failed — %v", err),
			TokenData: tokenData,
			TokenHash: thash,
			Guest:     guest,
			LogMsg:    fmt.Sprintf("Reject: delegated bootstrap failed: %v", err),
		}
	}

	seconds := int(state.AllotmentMs / 1000)
	if seconds <= 0 {
		return AuthDecision{
			Accept:    false,
			Error:     "zero allotment from server",
			TokenData: tokenData,
			TokenHash: thash,
			Guest:     guest,
			LogMsg:    fmt.Sprintf("Reject: zero allotment (allotmentMs=%d)", state.AllotmentMs),
		}
	}

	return AuthDecision{
		Accept:    true,
		Guest:     guest,
		TokenData: tokenData,
		Seconds:   seconds,
		TokenHash: thash,
		MOTD:      renderMOTD(tokenData, guest, seconds),
		LogMsg:    fmt.Sprintf("Delegated accept: guest=%s duration=%ds allotment=%dms", guest, seconds, state.AllotmentMs),
	}
}

// --- Helpers ---

func guestUsername(tokenStr string) string {
	return "g-" + cashu.TokenHash(tokenStr)[:8]
}

func safeHashPrefix(thash string) string {
	if len(thash) >= 16 {
		return thash[:16]
	}
	return thash
}

func renderMOTD(tokenData *cashu.TokenData, guest string, seconds int) string {
	minutes := tokenData.Amount
	isTest := isTestMint(tokenData.Mint)
	mintDisplay := tokenData.Mint
	if len(mintDisplay) > 36 {
		mintDisplay = mintDisplay[:33] + "..."
	}
	testStr := "NO"
	if isTest {
		testStr = "YES"
	}

	return fmt.Sprintf(
		"\r\n"+
			"  +======================================+\r\n"+
			"  |        CASHU TOLLGATE                |\r\n"+
			"  +======================================+\r\n"+
			"  |  Mint:   %-28s |\r\n"+
			"  |  Amount: %4d %-23s |\r\n"+
			"  |  Time:   %4d min (%5d sec)       |\r\n"+
			"  |  User:   %-28s |\r\n"+
			"  |  Test:   %-28s |\r\n"+
			"  +======================================+\r\n"+
			"  |  Type 'timeleft' to see time left    |\r\n"+
			"  |  Session ends when time runs out.    |\r\n"+
			"  +======================================+\r\n"+
			"\r\n",
		mintDisplay, minutes, tokenData.Unit, minutes, seconds, guest, testStr,
	)
}

func isTestMint(mintURL string) bool {
	return strings.Contains(strings.ToLower(mintURL), "test")
}
