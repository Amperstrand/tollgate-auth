package cashu

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- RedeemToken input validation ---
// We only test the validation logic (the actual cdk-cli subprocess can't be tested without it installed).

func TestRedeemToken_RejectsTooLongToken(t *testing.T) {
	longToken := "cashuA" + strings.Repeat("x", 5000)
	err := RedeemToken(longToken, "/tmp/nonexistent")
	if err == nil {
		t.Fatal("expected error for too-long token")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Errorf("error = %q, want 'too long'", err.Error())
	}
}

func TestRedeemToken_RejectsTokenAtBoundary(t *testing.T) {
	// Exactly 4096 chars — the limit is > 4096, so 4096 should be OK
	// but 4097 (prefix + 4091 x's) should fail
	// len("cashuA" + 4091 x's) = 4097 > 4096
	token := "cashuA" + strings.Repeat("a", 4091)
	if len(token) <= 4096 {
		// Token is at or under the limit — should proceed past validation
		// (will fail at exec stage, which is fine)
		err := RedeemToken(token, "/tmp/nonexistent")
		// The error should NOT be "too long"
		if err != nil && strings.Contains(err.Error(), "too long") {
			t.Error("4096-byte token should not be rejected as too long")
		}
	}
}

func TestRedeemToken_RejectsOverLimit(t *testing.T) {
	token := "cashuA" + strings.Repeat("a", 4091) // 4097 total
	if len(token) <= 4096 {
		t.Fatal("test token should be over 4096 bytes")
	}
	err := RedeemToken(token, "/tmp/nonexistent")
	if err == nil {
		t.Fatal("expected error for token over 4096 bytes")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Errorf("error = %q, want 'too long'", err.Error())
	}
}

func TestRedeemToken_RejectsInvalidPrefix(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"no prefix", "abcdef"},
		{"wrong prefix", "cashuCabc"},
		{"empty string", ""},
		{"just cashu", "cashu"},
		{"lowercase a", "cashuaabc"},
		{"lowercase b", "cashubabc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := RedeemToken(tc.token, "/tmp/nonexistent")
			if err == nil {
				t.Errorf("expected error for %q", tc.token)
			}
			if !strings.Contains(err.Error(), "invalid token prefix") {
				t.Errorf("error = %q, want 'invalid token prefix'", err.Error())
			}
		})
	}
}

func TestRedeemToken_AcceptsCashuAPrefix(t *testing.T) {
	// A valid-length cashuA token — will fail at exec (cdk-cli not installed)
	// but should NOT fail at the prefix check
	token := "cashuA" + strings.Repeat("a", 100)
	err := RedeemToken(token, "/tmp/nonexistent")
	// Should fail, but NOT because of prefix validation
	if err != nil && strings.Contains(err.Error(), "invalid token prefix") {
		t.Error("cashuA prefix should be accepted by validation")
	}
}

func TestRedeemToken_AcceptsCashuBPrefix(t *testing.T) {
	token := "cashuB" + strings.Repeat("a", 100)
	err := RedeemToken(token, "/tmp/nonexistent")
	if err != nil && strings.Contains(err.Error(), "invalid token prefix") {
		t.Error("cashuB prefix should be accepted by validation")
	}
}

// --- StoreInWallet ---

func TestStoreInWallet_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	walletFile := filepath.Join(dir, "wallet.jsonl")

	td := &TokenData{
		Mint:   "https://testnut.cashu.space",
		Amount: 8,
		Unit:   "sat",
	}

	StoreInWallet("cashuAtest123", td, walletFile)

	if _, err := os.Stat(walletFile); os.IsNotExist(err) {
		t.Fatal("wallet file was not created")
	}
}

func TestStoreInWallet_WritesValidJSONL(t *testing.T) {
	dir := t.TempDir()
	walletFile := filepath.Join(dir, "wallet.jsonl")

	td := &TokenData{
		Mint:   "https://testnut.cashu.space",
		Amount: 16,
		Unit:   "sat",
	}

	StoreInWallet("cashuAtest456", td, walletFile)

	f, err := os.Open(walletFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("wallet file is empty")
	}

	var entry map[string]interface{}
	if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if entry["mint"] != "https://testnut.cashu.space" {
		t.Errorf("mint = %v, want https://testnut.cashu.space", entry["mint"])
	}
	if entry["amount"] != float64(16) {
		t.Errorf("amount = %v, want 16", entry["amount"])
	}
	if entry["unit"] != "sat" {
		t.Errorf("unit = %v, want sat", entry["unit"])
	}
	if entry["token"] != "cashuAtest456" {
		t.Errorf("token = %v, want cashuAtest456", entry["token"])
	}
}

func TestStoreInWallet_AppendsMultiple(t *testing.T) {
	dir := t.TempDir()
	walletFile := filepath.Join(dir, "wallet.jsonl")

	td1 := &TokenData{Mint: "https://mint1.example.com", Amount: 4, Unit: "sat"}
	td2 := &TokenData{Mint: "https://mint2.example.com", Amount: 8, Unit: "sat"}

	StoreInWallet("token1", td1, walletFile)
	StoreInWallet("token2", td2, walletFile)

	data, err := os.ReadFile(walletFile)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var e1, e2 map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &e1); err != nil {
		t.Fatalf("line 1 invalid JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &e2); err != nil {
		t.Fatalf("line 2 invalid JSON: %v", err)
	}
	if e1["token"] != "token1" {
		t.Errorf("line 1 token = %v, want token1", e1["token"])
	}
	if e2["token"] != "token2" {
		t.Errorf("line 2 token = %v, want token2", e2["token"])
	}
}

func TestStoreInWallet_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	walletFile := filepath.Join(dir, "wallet.jsonl")

	td := &TokenData{Mint: "https://test.example.com", Amount: 1, Unit: "sat"}
	StoreInWallet("tok", td, walletFile)

	info, err := os.Stat(walletFile)
	if err != nil {
		t.Fatal(err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}
}
