package cashu

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// VerifyWithMint checks that all proofs in the token are unspent.
// For non-test mints, it skips verification (returns true).
func VerifyWithMint(tokenData *TokenData) (bool, string) {
	mintURL := strings.TrimRight(tokenData.Mint, "/")
	isTest := strings.Contains(strings.ToLower(mintURL), "test")

	if !strings.HasPrefix(mintURL, "http") {
		return false, "Invalid mint URL"
	}

	if !isTest {
		log.Printf("Real-mint token (no enforcement): %s", mintURL)
		return true, "OK"
	}

	reqBody := CheckStateRequest{}
	for _, p := range tokenData.Proofs {
		reqBody.Proofs = append(reqBody.Proofs, struct {
			Secret string `json:"secret"`
		}{Secret: p.Secret})
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return false, fmt.Sprintf("JSON error: %v", err)
	}

	if !isSafeMintURL(mintURL) {
		return false, "Mint URL rejected (SSRF protection)"
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(mintURL+"/v1/checkstate", "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return false, fmt.Sprintf("Mint unreachable: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false, fmt.Sprintf("Mint error: HTTP %d", resp.StatusCode)
	}

	var result CheckStateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Sprintf("Mint response parse error: %v", err)
	}

	for _, s := range result.States {
		if s.State != "UNSPENT" {
			return false, "Token already spent"
		}
	}

	return true, "OK"
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
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
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
