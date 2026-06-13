package operator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validTestNsec is a valid nsec for testing (generated from known key bytes).
var validTestNsec = mustEncodeTestNsec([32]byte{
	0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
	0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
	0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
	0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
})

func mustEncodeTestNsec(key [32]byte) string {
	s, err := encodeNsecForTest(key[:])
	if err != nil {
		panic(err)
	}
	return s
}

func writeTestRegistry(t *testing.T, entries []registryOperatorEntry) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "operators.json")

	cfg := registryConfigFile{Operators: entries}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}
	return path
}

func TestResolver_NoConfigNoEnv(t *testing.T) {
	// No registry file, no env npub → anonymous operator
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", "")
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")
	t.Setenv("TOLLGATE_OPERATOR_NSEC", validTestNsec)

	r, err := NewResolver("")
	if err != nil {
		t.Fatalf("NewResolver() error: %v", err)
	}

	ctx := r.Resolve("", "")
	if ctx == nil {
		t.Fatal("Resolve() returned nil, want anonymous context")
	}
	if ctx.Account.ID != "anonymous" {
		t.Errorf("Account.ID = %q, want %q", ctx.Account.ID, "anonymous")
	}
	if ctx.Resolved {
		t.Error("Resolved = true, want false for anonymous")
	}
	if ctx.Source != "default" {
		t.Errorf("Source = %q, want %q", ctx.Source, "default")
	}
}

func TestResolver_EnvNpubOnly(t *testing.T) {
	// No registry file, env npub set → resolved from env
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", "")
	t.Setenv("TOLLGATE_OPERATOR_NPUB", validNPub)
	t.Setenv("TOLLGATE_OPERATOR_NSEC", validTestNsec)

	r, err := NewResolver("")
	if err != nil {
		t.Fatalf("NewResolver() error: %v", err)
	}

	ctx := r.Resolve("", "")
	if ctx == nil {
		t.Fatal("Resolve() returned nil")
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

func TestResolver_ConfigClientIPMatch(t *testing.T) {
	// Config file with matching client_ip → resolved from config
	entries := []registryOperatorEntry{
		{
			ID:         "op-cafe",
			PayoutNpub: validNPub,
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				ClientIP: "192.168.1.100",
			},
		},
	}

	registryPath := writeTestRegistry(t, entries)
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", registryPath)
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")
	t.Setenv("TOLLGATE_OPERATOR_NSEC", validTestNsec)

	r, err := NewResolver("")
	if err != nil {
		t.Fatalf("NewResolver() error: %v", err)
	}

	ctx := r.Resolve("192.168.1.100", "")
	if ctx == nil {
		t.Fatal("Resolve() returned nil")
	}
	if ctx.Account.ID != "op-cafe" {
		t.Errorf("Account.ID = %q, want %q", ctx.Account.ID, "op-cafe")
	}
	if ctx.Source != "config-file" {
		t.Errorf("Source = %q, want %q", ctx.Source, "config-file")
	}
}

func TestResolver_ConfigNASIDMatch(t *testing.T) {
	// Config file with matching nas_id → resolved from config
	entries := []registryOperatorEntry{
		{
			ID:         "op-hotel",
			PayoutNpub: validNPub,
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				NASID: "hotel-ap-1",
			},
		},
	}

	registryPath := writeTestRegistry(t, entries)
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", registryPath)
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")
	t.Setenv("TOLLGATE_OPERATOR_NSEC", validTestNsec)

	r, err := NewResolver("")
	if err != nil {
		t.Fatalf("NewResolver() error: %v", err)
	}

	ctx := r.Resolve("", "hotel-ap-1")
	if ctx == nil {
		t.Fatal("Resolve() returned nil")
	}
	if ctx.Account.ID != "op-hotel" {
		t.Errorf("Account.ID = %q, want %q", ctx.Account.ID, "op-hotel")
	}
}

func TestResolver_ConfigDefaultFallback(t *testing.T) {
	// Config file with default operator → falls back to default when no match
	entries := []registryOperatorEntry{
		{
			ID:         "op-specific",
			PayoutNpub: "npub1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				ClientIP: "10.0.0.1",
			},
		},
		{
			ID:         "op-default",
			PayoutNpub: "npub1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				Default: true,
			},
		},
	}

	registryPath := writeTestRegistry(t, entries)
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", registryPath)
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")
	t.Setenv("TOLLGATE_OPERATOR_NSEC", validTestNsec)

	r, err := NewResolver("")
	if err != nil {
		t.Fatalf("NewResolver() error: %v", err)
	}

	ctx := r.Resolve("unknown-ip", "unknown-nas")
	if ctx == nil {
		t.Fatal("Resolve() returned nil, want default operator")
	}
	if ctx.Account.ID != "op-default" {
		t.Errorf("Account.ID = %q, want %q (default fallback)", ctx.Account.ID, "op-default")
	}
}

func TestResolver_NoMatchNoDefault(t *testing.T) {
	// Config file with no matching entry and no default → anonymous
	entries := []registryOperatorEntry{
		{
			ID:         "op-specific",
			PayoutNpub: validNPub,
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				ClientIP: "10.0.0.1",
			},
		},
	}

	registryPath := writeTestRegistry(t, entries)
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", registryPath)
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")
	t.Setenv("TOLLGATE_OPERATOR_NSEC", validTestNsec)

	r, err := NewResolver("")
	if err != nil {
		t.Fatalf("NewResolver() error: %v", err)
	}

	ctx := r.Resolve("unknown-ip", "unknown-nas")
	if ctx == nil {
		t.Fatal("Resolve() returned nil, want anonymous context")
	}
	if ctx.Account.ID != "anonymous" {
		t.Errorf("Account.ID = %q, want %q (no match, no default → anonymous)", ctx.Account.ID, "anonymous")
	}
}

func TestResolver_HMACKeyFromNsec_Deterministic(t *testing.T) {
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", "")
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")
	t.Setenv("TOLLGATE_OPERATOR_NSEC", validTestNsec)

	r1, err := NewResolver("")
	if err != nil {
		t.Fatalf("NewResolver() error: %v", err)
	}
	r2, err := NewResolver("")
	if err != nil {
		t.Fatalf("NewResolver() error: %v", err)
	}

	key1 := r1.HMACKey()
	key2 := r2.HMACKey()

	if len(key1) != 32 {
		t.Errorf("len(HMACKey()) = %d, want 32", len(key1))
	}
	if string(key1) != string(key2) {
		t.Errorf("HMACKey() differs between calls with same nsec: %x vs %x", key1, key2)
	}
}

func TestResolver_NoNsec_HardError(t *testing.T) {
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", "")
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")
	t.Setenv("TOLLGATE_OPERATOR_NSEC", "")

	_, err := NewResolver("")
	if err == nil {
		t.Fatal("NewResolver() without nsec expected error, got nil")
	}
	if !strings.Contains(err.Error(), "TOLLGATE_OPERATOR_NSEC") {
		t.Errorf("error should mention TOLLGATE_OPERATOR_NSEC, got: %v", err)
	}
}

func TestResolver_InvalidNsec(t *testing.T) {
	t.Setenv("TOLLGATE_OPERATOR_NSEC", "not-a-valid-nsec!")
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", "")
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")

	_, err := NewResolver("")
	if err == nil {
		t.Fatal("NewResolver() with invalid nsec expected error, got nil")
	}
}

func TestResolver_NsecWithWrongPrefix(t *testing.T) {
	// Generate a valid bech32 value but with npub prefix instead of nsec
	npubValue := strings.Replace(validTestNsec, "nsec1", "npub1", 1)
	t.Setenv("TOLLGATE_OPERATOR_NSEC", npubValue)
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", "")
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")

	_, err := NewResolver("")
	if err == nil {
		t.Fatal("NewResolver() with npub prefix expected error, got nil")
	}
}

func TestResolver_ConfigPathArgument(t *testing.T) {
	// When TOLLGATE_OPERATOR_REGISTRY is not set, use configPath argument
	entries := []registryOperatorEntry{
		{
			ID:         "op-arg-path",
			PayoutNpub: validNPub,
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				Default: true,
			},
		},
	}

	registryPath := writeTestRegistry(t, entries)
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", "")
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")
	t.Setenv("TOLLGATE_OPERATOR_NSEC", validTestNsec)

	r, err := NewResolver(registryPath)
	if err != nil {
		t.Fatalf("NewResolver() error: %v", err)
	}

	ctx := r.Resolve("any", "any")
	if ctx == nil {
		t.Fatal("Resolve() returned nil")
	}
	if ctx.Account.ID != "op-arg-path" {
		t.Errorf("Account.ID = %q, want %q", ctx.Account.ID, "op-arg-path")
	}
}

func TestResolver_EnvRegistryOverridesConfigPath(t *testing.T) {
	// TOLLGATE_OPERATOR_REGISTRY env overrides configPath argument
	entries1 := []registryOperatorEntry{
		{
			ID:         "op-from-arg",
			PayoutNpub: validNPub,
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				Default: true,
			},
		},
	}
	entries2 := []registryOperatorEntry{
		{
			ID:         "op-from-env",
			PayoutNpub: "npub1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				Default: true,
			},
		},
	}

	argPath := writeTestRegistry(t, entries1)
	envPath := writeTestRegistry(t, entries2)

	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", envPath)
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")
	t.Setenv("TOLLGATE_OPERATOR_NSEC", validTestNsec)

	r, err := NewResolver(argPath)
	if err != nil {
		t.Fatalf("NewResolver() error: %v", err)
	}

	ctx := r.Resolve("any", "any")
	if ctx == nil {
		t.Fatal("Resolve() returned nil")
	}
	if ctx.Account.ID != "op-from-env" {
		t.Errorf("Account.ID = %q, want %q (env should override arg)", ctx.Account.ID, "op-from-env")
	}
}
