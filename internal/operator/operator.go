package operator

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// OperatorAccount identifies the gateway operator (the person collecting payments).
type OperatorAccount struct {
	ID            string    // stable identifier (e.g. "op-abc123")
	PayoutNpub    string    // Nostr npub for payout (public identifier, NOT a secret)
	PayoutLNURL   string    // optional LNURL-pay for Lightning payout
	PayoutAddress string    // optional on-chain address
	CreatedAt     time.Time
}

// OperatorContext carries the resolved operator through the auth pipeline.
type OperatorContext struct {
	Account  OperatorAccount
	Source   string // "config-file", "env", "inline-arg"
	Resolved bool   // true if operator was successfully identified
}

// operatorConfig is the JSON structure for the config file.
type operatorConfig struct {
	ID            string `json:"id"`
	PayoutNpub    string `json:"payout_npub"`
	PayoutLNURL   string `json:"payout_lnurl"`
	PayoutAddress string `json:"payout_address"`
}

// isBech32Char returns true if the byte is a valid bech32 character
// (a-z, 0-9, A-Z as used in bech32m encoding after the separator).
func isBech32Char(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	default:
		return false
	}
}

// ParseOperatorNpub validates an npub string and returns an OperatorAccount.
// The npub must:
//   - Start with "npub1"
//   - Contain only bech32 characters (a-z, 0-9, A-Z)
//   - Be exactly 63 characters total (npub1 + 58 chars)
func ParseOperatorNpub(s string) (OperatorAccount, error) {
	if !strings.HasPrefix(s, "npub1") {
		return OperatorAccount{}, fmt.Errorf("invalid npub: must start with \"npub1\"")
	}

	if len(s) != 63 {
		return OperatorAccount{}, fmt.Errorf("invalid npub: must be 63 characters, got %d", len(s))
	}

	for i := 4; i < len(s); i++ {
		if !isBech32Char(s[i]) {
			return OperatorAccount{}, fmt.Errorf("invalid npub: invalid character at position %d", i)
		}
	}

	return OperatorAccount{
		PayoutNpub: s,
		CreatedAt:  time.Now(),
	}, nil
}

// ParseOperatorFromEnv reads the TOLLGATE_OPERATOR_NPUB environment variable
// and returns an OperatorContext if set. Returns (nil, nil) if the variable
// is empty or missing — this is not an error condition.
func ParseOperatorFromEnv() (*OperatorContext, error) {
	npub := os.Getenv("TOLLGATE_OPERATOR_NPUB")
	if npub == "" {
		return nil, nil
	}

	acc, err := ParseOperatorNpub(npub)
	if err != nil {
		return nil, fmt.Errorf("parsing operator npub from env: %w", err)
	}

	return &OperatorContext{
		Account:  acc,
		Source:   "env",
		Resolved: true,
	}, nil
}

// ParseOperatorFromConfig reads a JSON config file and returns an
// OperatorContext. Returns (nil, nil) if the file doesn't exist.
// Returns an error if the file exists but contains invalid JSON.
func ParseOperatorFromConfig(path string) (*OperatorContext, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading operator config: %w", err)
	}

	var cfg operatorConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing operator config: %w", err)
	}

	acc := OperatorAccount{
		ID:            cfg.ID,
		PayoutNpub:    cfg.PayoutNpub,
		PayoutLNURL:   cfg.PayoutLNURL,
		PayoutAddress: cfg.PayoutAddress,
		CreatedAt:     time.Now(),
	}

	return &OperatorContext{
		Account:  acc,
		Source:   "config-file",
		Resolved: true,
	}, nil
}

// ValidateOperatorAccount ensures that at least one payout destination
// is set on the account (npub, LNURL, or on-chain address).
func ValidateOperatorAccount(acc OperatorAccount) error {
	if acc.PayoutNpub != "" {
		return nil
	}
	if acc.PayoutLNURL != "" {
		return nil
	}
	if acc.PayoutAddress != "" {
		return nil
	}
	return fmt.Errorf("operator account has no payout destination: must set at least one of npub, lnurl, or address")
}
