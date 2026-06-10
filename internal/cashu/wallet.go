package cashu

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// RedeemToken calls cdk-cli to redeem a token into the wallet.
//
// cdk-cli v0.16.0 output varies by token type:
//   - Full DLEQ tokens: "Recovered 0 operations, N compensated, ..." then exits code 1
//     (bug: prints "Error: code: 14005" after success)
//   - No-DLEQ tokens: "Received: N" then exits code 0
//
// We check output for both "compensated" and "Received:" indicators.
func RedeemToken(tokenStr string, walletDir string) error {
	cmd := exec.Command(
		"/usr/bin/sudo", "-u", "cashu-wallet",
		"/usr/local/bin/cdk-cli",
		"--work-dir", walletDir,
		"receive",
		"--allow-untrusted",
		tokenStr,
	)
	out, _ := cmd.CombinedOutput()
	output := string(out)

	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "compensated") {
			var compensated, skipped, failed int
			fmt.Sscanf(line, "Recovered %d operations, %d compensated, %d skipped, %d failed", new(int), &compensated, &skipped, &failed)
			if compensated > 0 {
				log.Printf("cdk-cli receive: %s", strings.TrimSpace(line))
				return nil
			}
			if skipped > 0 {
				return fmt.Errorf("cdk-cli receive: token already spent (skipped)")
			}
			if failed > 0 {
				return fmt.Errorf("cdk-cli receive: token redemption failed")
			}
		}
		if strings.HasPrefix(strings.TrimSpace(line), "Received:") {
			var received int
			fmt.Sscanf(strings.TrimSpace(line), "Received: %d", &received)
			if received > 0 {
				log.Printf("cdk-cli receive: %s", strings.TrimSpace(line))
				return nil
			}
		}
	}
	return fmt.Errorf("cdk-cli receive failed: no compensation reported: %s", strings.TrimSpace(output))
}

// StoreInWallet appends the token to the wallet JSONL file.
func StoreInWallet(tokenStr string, tokenData *TokenData, walletFile string) {
	entry := map[string]interface{}{
		"ts":     tokenData.Mint, // placeholder, caller should set ts
		"mint":   tokenData.Mint,
		"amount": tokenData.Amount,
		"unit":   tokenData.Unit,
		"token":  tokenStr,
	}
	f, err := os.OpenFile(walletFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(entry)
	f.WriteString(string(line) + "\n")
	log.Printf("Wallet +%d %s from %s", tokenData.Amount, tokenData.Unit, tokenData.Mint)
}
