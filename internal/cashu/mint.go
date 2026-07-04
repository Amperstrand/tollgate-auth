package cashu

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ProofState represents the NUT-07 state of a token's proofs at the mint.
type ProofState string

const (
	StateUnspent ProofState = "UNSPENT"
	StateSpent   ProofState = "SPENT"
	StatePending ProofState = "PENDING"
)

// CheckTokenState queries the mint (NUT-07 /v1/checkstate) for the state of
// all proofs in the token. Returns the aggregate state:
//   - SPENT if any proof is SPENT
//   - PENDING if any proof is PENDING (and none SPENT)
//   - UNSPENT if all proofs are UNSPENT
//
// For non-test mints, returns UNSPENT without checking (matching legacy behavior).
// On error (unreachable mint, parse failure), returns "" + error message.
func CheckTokenState(tokenData *TokenData) (ProofState, string) {
	mintURL := strings.TrimRight(tokenData.Mint, "/")

	if !strings.HasPrefix(mintURL, "http") {
		return "", "Invalid mint URL"
	}

	if !isSafeMintURL(mintURL) {
		return "", "Mint URL rejected (SSRF protection)"
	}

	reqBody := CheckStateRequest{}
	for _, p := range tokenData.Proofs {
		yBytes, err := HashToCurve(p.Secret)
		if err != nil {
			return "", fmt.Sprintf("hash_to_curve failed for proof: %v", err)
		}
		reqBody.Ys = append(reqBody.Ys, fmt.Sprintf("%x", yBytes))
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Sprintf("JSON error: %v", err)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(mintURL+"/v1/checkstate", "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Sprintf("Mint unreachable: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Sprintf("Mint error: HTTP %d", resp.StatusCode)
	}

	var result CheckStateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Sprintf("Mint response parse error: %v", err)
	}

	hasSpent := false
	hasPending := false
	for _, s := range result.States {
		switch s.State {
		case "SPENT":
			hasSpent = true
		case "PENDING":
			hasPending = true
		}
	}

	if hasSpent {
		return StateSpent, "Token proofs are spent"
	}
	if hasPending {
		return StatePending, "Token proofs are pending (swap in progress)"
	}
	return StateUnspent, "OK"
}

// VerifyWithMint checks that all proofs in the token are unspent.
// Returns true only if all proofs are UNSPENT.
// Deprecated: Use CheckTokenState for recovery-aware state machine logic.
func VerifyWithMint(tokenData *TokenData) (bool, string) {
	state, msg := CheckTokenState(tokenData)
	return state == StateUnspent, msg
}

// isSafeMintURL blocks SSRF attempts by rejecting private/internal IP ranges.
// The mint URL comes from the decoded Cashu token, which is attacker-controlled.
func isSafeMintURL(mintURL string) bool {
	if !strings.HasPrefix(mintURL, "https://") && !strings.HasPrefix(mintURL, "http://") {
		return false
	}
	u, err := url.Parse(mintURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || host == "::1" {
		return false
	}
	if host == "127.0.0.1" && os.Getenv("ALLOW_LOCALHOST_MINT") == "true" {
		return true
	}
	if host == "127.0.0.1" {
		return false
	}
	if strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "169.254.") {
		return false
	}
	if strings.HasPrefix(host, "172.") {
		parts := strings.SplitN(host, ".", 3)
		if len(parts) >= 2 {
			second, _ := strconv.Atoi(parts[1])
			if second >= 16 && second <= 31 {
				return false
			}
		}
	}
	return true
}
