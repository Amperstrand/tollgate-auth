package main

import (
	"encoding/base64"
	"testing"
)

// validWgPubkey returns a real-looking 44-char base64 pubkey that decodes
// to 32 bytes — the exact shape `wg set peer` will accept.
func validWgPubkey(t *testing.T) string {
	t.Helper()
	// 32 zero bytes → base64 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := validateWgPubkey(key); err != nil {
		t.Fatalf("fixture key should validate: %v", err)
	}
	return key
}

func TestValidateWgPubkey_accepts_real_pubkey(t *testing.T) {
	if err := validateWgPubkey(validWgPubkey(t)); err != nil {
		t.Errorf("expected real-shaped pubkey to pass, got: %v", err)
	}
}

func TestValidateWgPubkey_rejects_empty(t *testing.T) {
	if err := validateWgPubkey(""); err == nil {
		t.Error("expected empty pubkey to be rejected")
	}
}

func TestValidateWgPubkey_rejects_shell_metachars(t *testing.T) {
	attacks := []string{
		"peer; rm -rf /",
		"$(touch /tmp/pwned)",
		"| cat /etc/passwd",
		"`id`",
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= ; rm /tmp/x",
	}
	for _, attack := range attacks {
		t.Run(attack, func(t *testing.T) {
			if err := validateWgPubkey(attack); err == nil {
				t.Errorf("expected %q to be rejected", attack)
			}
		})
	}
}

func TestValidateWgPubkey_rejects_wrong_length(t *testing.T) {
	cases := []string{
		"AAAA", // too short
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",    // 43 chars, no padding
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==", // 44 chars but double padding
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if err := validateWgPubkey(c); err == nil {
				t.Errorf("expected %q (len=%d) to be rejected", c, len(c))
			}
		})
	}
}

func TestValidateWgPubkey_rejects_bad_base64(t *testing.T) {
	// 44 chars but contains invalid base64 char '@'
	if err := validateWgPubkey("@AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="); err == nil {
		t.Error("expected bad-base64 pubkey to be rejected")
	}
}
