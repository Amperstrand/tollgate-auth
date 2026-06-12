package operator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validNPub is a properly-formatted npub for tests (63 chars: "npub1" + 58 lowercase).
const validNPub = "npub1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestParseOperatorNpub_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"lowercase", validNPub},
		{"mixed case bech32m", "npub1AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"alphanumeric", "npub1a0123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwx"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc, err := ParseOperatorNpub(tt.input)
			if err != nil {
				t.Fatalf("ParseOperatorNpub(%q) returned error: %v", tt.input, err)
			}
			if acc.PayoutNpub != tt.input {
				t.Errorf("PayoutNpub = %q, want %q", acc.PayoutNpub, tt.input)
			}
			if acc.CreatedAt.IsZero() {
				t.Error("CreatedAt is zero, expected non-zero timestamp")
			}
		})
	}
}

func TestParseOperatorNpub_InvalidPrefix(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"wrong prefix npub0", "npub0" + strings.Repeat("a", 58)},
		{"wrong prefix npub2", "npub2" + strings.Repeat("a", 58)},
		{"no prefix", strings.Repeat("a", 63)},
		{"npub without 1", "npub" + strings.Repeat("a", 59)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseOperatorNpub(tt.input)
			if err == nil {
				t.Fatalf("ParseOperatorNpub(%q) expected error, got nil", tt.input)
			}
			if !strings.Contains(err.Error(), "must start with") {
				t.Errorf("error = %q, want message about prefix", err.Error())
			}
		})
	}
}

func TestParseOperatorNpub_InvalidLength(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantLen int
	}{
		{"too short", "npub1" + strings.Repeat("a", 10), 14},
		{"one char short", "npub1" + strings.Repeat("a", 57), 61},
		{"one char long", "npub1" + strings.Repeat("a", 59), 63},
		{"way too long", "npub1" + strings.Repeat("a", 100), 104},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseOperatorNpub(tt.input)
			if err == nil {
				t.Fatalf("ParseOperatorNpub(%q) expected error, got nil", tt.input)
			}
			if !strings.Contains(err.Error(), "63 characters") {
				t.Errorf("error = %q, want message about length", err.Error())
			}
		})
	}
}

func TestParseOperatorNpub_InvalidChars(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"space", "npub1" + strings.Repeat("a", 57) + " "},
		{"special char !", "npub1" + strings.Repeat("a", 57) + "!"},
		{"special char @", "npub1" + strings.Repeat("a", 57) + "@"},
		{"newline", "npub1" + strings.Repeat("a", 57) + "\n"},
		{"hyphen", "npub1" + strings.Repeat("a", 57) + "-"},
		{"percent sign", "npub1" + strings.Repeat("a", 57) + "%"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseOperatorNpub(tt.input)
			if err == nil {
				t.Fatalf("ParseOperatorNpub(%q) expected error, got nil", tt.input)
			}
			if !strings.Contains(err.Error(), "invalid character") {
				t.Errorf("error = %q, want message about invalid character", err.Error())
			}
		})
	}
}

func TestParseOperatorFromEnv_Set(t *testing.T) {
	t.Setenv("TOLLGATE_OPERATOR_NPUB", validNPub)

	ctx, err := ParseOperatorFromEnv()
	if err != nil {
		t.Fatalf("ParseOperatorFromEnv() returned error: %v", err)
	}
	if ctx == nil {
		t.Fatal("ParseOperatorFromEnv() returned nil context")
	}
	if !ctx.Resolved {
		t.Error("Resolved = false, want true")
	}
	if ctx.Source != "env" {
		t.Errorf("Source = %q, want %q", ctx.Source, "env")
	}
	if ctx.Account.PayoutNpub != validNPub {
		t.Errorf("PayoutNpub = %q, want %q", ctx.Account.PayoutNpub, validNPub)
	}
}

func TestParseOperatorFromEnv_Unset(t *testing.T) {
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")

	ctx, err := ParseOperatorFromEnv()
	if err != nil {
		t.Fatalf("ParseOperatorFromEnv() with empty env returned error: %v", err)
	}
	if ctx != nil {
		t.Errorf("ParseOperatorFromEnv() with empty env = %+v, want nil", ctx)
	}
}

func TestParseOperatorFromEnv_InvalidNpub(t *testing.T) {
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "not-a-valid-npub")

	_, err := ParseOperatorFromEnv()
	if err == nil {
		t.Fatal("ParseOperatorFromEnv() with invalid npub expected error, got nil")
	}
	if !strings.Contains(err.Error(), "parsing operator npub from env") {
		t.Errorf("error = %q, want wrapped error from env parsing", err.Error())
	}
}

func TestParseOperatorFromConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "operator.json")

	cfg := operatorConfig{
		ID:            "op-test123",
		PayoutNpub:    validNPub,
		PayoutLNURL:   "lnurl1dp68gurn8ghj7ampd3kx2ar0veekzar0wd5xjtnrdakj7",
		PayoutAddress: "bc1qexample",
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	ctx, err := ParseOperatorFromConfig(path)
	if err != nil {
		t.Fatalf("ParseOperatorFromConfig() returned error: %v", err)
	}
	if ctx == nil {
		t.Fatal("ParseOperatorFromConfig() returned nil context")
	}
	if !ctx.Resolved {
		t.Error("Resolved = false, want true")
	}
	if ctx.Source != "config-file" {
		t.Errorf("Source = %q, want %q", ctx.Source, "config-file")
	}
	if ctx.Account.ID != "op-test123" {
		t.Errorf("ID = %q, want %q", ctx.Account.ID, "op-test123")
	}
	if ctx.Account.PayoutNpub != validNPub {
		t.Errorf("PayoutNpub = %q, want %q", ctx.Account.PayoutNpub, validNPub)
	}
	if ctx.Account.PayoutLNURL != cfg.PayoutLNURL {
		t.Errorf("PayoutLNURL = %q, want %q", ctx.Account.PayoutLNURL, cfg.PayoutLNURL)
	}
	if ctx.Account.PayoutAddress != "bc1qexample" {
		t.Errorf("PayoutAddress = %q, want %q", ctx.Account.PayoutAddress, "bc1qexample")
	}
}

func TestParseOperatorFromConfig_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	ctx, err := ParseOperatorFromConfig(path)
	if err != nil {
		t.Fatalf("ParseOperatorFromConfig() with missing file returned error: %v", err)
	}
	if ctx != nil {
		t.Errorf("ParseOperatorFromConfig() with missing file = %+v, want nil", ctx)
	}
}

func TestParseOperatorFromConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "operator.json")

	if err := os.WriteFile(path, []byte("{not valid json}"), 0600); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	_, err := ParseOperatorFromConfig(path)
	if err == nil {
		t.Fatal("ParseOperatorFromConfig() with invalid JSON expected error, got nil")
	}
	if !strings.Contains(err.Error(), "parsing operator config") {
		t.Errorf("error = %q, want message about parsing config", err.Error())
	}
}

func TestValidateOperatorAccount_NoPayout(t *testing.T) {
	acc := OperatorAccount{
		ID: "op-xyz",
	}

	err := ValidateOperatorAccount(acc)
	if err == nil {
		t.Fatal("ValidateOperatorAccount() with no payout expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no payout destination") {
		t.Errorf("error = %q, want message about no payout destination", err.Error())
	}
}

func TestValidateOperatorAccount_WithNpub(t *testing.T) {
	acc := OperatorAccount{
		ID:         "op-xyz",
		PayoutNpub: validNPub,
	}

	if err := ValidateOperatorAccount(acc); err != nil {
		t.Errorf("ValidateOperatorAccount() with npub returned error: %v", err)
	}
}

func TestValidateOperatorAccount_WithLNURL(t *testing.T) {
	acc := OperatorAccount{
		ID:          "op-xyz",
		PayoutLNURL: "lnurl1dp68gurn8ghj7ampd3kx2ar0veekzar0wd5xjtnrdakj7",
	}

	if err := ValidateOperatorAccount(acc); err != nil {
		t.Errorf("ValidateOperatorAccount() with lnurl returned error: %v", err)
	}
}

func TestValidateOperatorAccount_WithAddress(t *testing.T) {
	acc := OperatorAccount{
		ID:            "op-xyz",
		PayoutAddress: "bc1qexample",
	}

	if err := ValidateOperatorAccount(acc); err != nil {
		t.Errorf("ValidateOperatorAccount() with address returned error: %v", err)
	}
}
