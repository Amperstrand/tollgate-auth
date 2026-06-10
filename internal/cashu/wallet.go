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
func RedeemToken(tokenStr string, walletDir string) error {
	cmd := exec.Command(
		"sudo", "-u", "cashu-wallet",
		"cdk-cli",
		"--work-dir", walletDir,
		"receive",
		"--allow-untrusted",
		tokenStr,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cdk-cli receive failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	result := strings.TrimSpace(string(out))
	log.Printf("cdk-cli receive: %s", result)
	return nil
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
