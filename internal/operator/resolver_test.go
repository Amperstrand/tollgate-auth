package operator

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
	t.Setenv("TOLLGATE_CLASS_HMAC_KEY", "0123456789abcdef0123456789abcdef")

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
	t.Setenv("TOLLGATE_CLASS_HMAC_KEY", "0123456789abcdef0123456789abcdef")

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
	t.Setenv("TOLLGATE_CLASS_HMAC_KEY", "0123456789abcdef0123456789abcdef")

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
	t.Setenv("TOLLGATE_CLASS_HMAC_KEY", "0123456789abcdef0123456789abcdef")

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
	t.Setenv("TOLLGATE_CLASS_HMAC_KEY", "0123456789abcdef0123456789abcdef")

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
	t.Setenv("TOLLGATE_CLASS_HMAC_KEY", "0123456789abcdef0123456789abcdef")

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

func TestResolver_HMACKeyDeterministic(t *testing.T) {
	// HMAC key is deterministic when set via env
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", "")
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")
	t.Setenv("TOLLGATE_CLASS_HMAC_KEY", "0123456789abcdef0123456789abcdef")

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

	expected, _ := hex.DecodeString("0123456789abcdef0123456789abcdef")
	if string(key1) != string(expected) {
		t.Errorf("HMACKey() = %x, want %x", key1, expected)
	}
	if string(key1) != string(key2) {
		t.Errorf("HMACKey() differs between calls: %x vs %x", key1, key2)
	}
}

func TestResolver_HMACKeyRandom(t *testing.T) {
	// Without env, random keys are generated (different each time)
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", "")
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")
	t.Setenv("TOLLGATE_CLASS_HMAC_KEY", "")

	r1, err := NewResolver("")
	if err != nil {
		t.Fatalf("NewResolver() error: %v", err)
	}

	r2, err := NewResolver("")
	if err != nil {
		t.Fatalf("NewResolver() error: %v", err)
	}

	if len(r1.HMACKey()) != 32 {
		t.Errorf("len(HMACKey()) = %d, want 32", len(r1.HMACKey()))
	}
	if string(r1.HMACKey()) == string(r2.HMACKey()) {
		t.Error("two NewResolver calls produced same random key (extremely unlikely)")
	}
}

func TestResolver_InvalidHMACKeyHex(t *testing.T) {
	t.Setenv("TOLLGATE_CLASS_HMAC_KEY", "not-valid-hex!")
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", "")
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")

	_, err := NewResolver("")
	if err == nil {
		t.Fatal("NewResolver() with invalid hex key expected error, got nil")
	}
}

func TestResolver_HMACKeyTooShort(t *testing.T) {
	t.Setenv("TOLLGATE_CLASS_HMAC_KEY", "aabb")
	t.Setenv("TOLLGATE_OPERATOR_REGISTRY", "")
	t.Setenv("TOLLGATE_OPERATOR_NPUB", "")

	_, err := NewResolver("")
	if err == nil {
		t.Fatal("NewResolver() with short key expected error, got nil")
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
	t.Setenv("TOLLGATE_CLASS_HMAC_KEY", "0123456789abcdef0123456789abcdef")

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
	t.Setenv("TOLLGATE_CLASS_HMAC_KEY", "0123456789abcdef0123456789abcdef")

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
