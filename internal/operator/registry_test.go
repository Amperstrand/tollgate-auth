package operator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeRegistryFile(t *testing.T, entries []registryOperatorEntry) string {
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

func TestLoadRegistry_ValidFile(t *testing.T) {
	entries := []registryOperatorEntry{
		{
			ID:         "op-cafe-1",
			PayoutNpub: validNPub,
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				ClientIP: "192.168.1.1",
				NASID:    "cafe-ap-1",
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

	path := writeRegistryFile(t, entries)
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry() returned error: %v", err)
	}
	if reg == nil {
		t.Fatal("LoadRegistry() returned nil registry")
	}
	ops := reg.Operators()
	if len(ops) != 2 {
		t.Fatalf("len(Operators()) = %d, want 2", len(ops))
	}
	if ops[0].Account.ID != "op-cafe-1" {
		t.Errorf("ops[0].Account.ID = %q, want %q", ops[0].Account.ID, "op-cafe-1")
	}
	if ops[0].Match.ClientIP != "192.168.1.1" {
		t.Errorf("ops[0].Match.ClientIP = %q, want %q", ops[0].Match.ClientIP, "192.168.1.1")
	}
	if ops[1].Match.Default != true {
		t.Error("ops[1].Match.Default = false, want true")
	}
	if reg.defaultOp == nil {
		t.Error("defaultOp is nil, expected non-nil")
	}
}

func TestLoadRegistry_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry() with missing file returned error: %v", err)
	}
	if reg == nil {
		t.Fatal("LoadRegistry() with missing file returned nil registry")
	}
	if len(reg.Operators()) != 0 {
		t.Errorf("len(Operators()) = %d, want 0", len(reg.Operators()))
	}
}

func TestLoadRegistry_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "operators.json")

	if err := os.WriteFile(path, []byte("{not valid json}"), 0600); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	_, err := LoadRegistry(path)
	if err == nil {
		t.Fatal("LoadRegistry() with invalid JSON expected error, got nil")
	}
}

func TestLoadRegistry_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "operators.json")

	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry() with empty JSON returned error: %v", err)
	}
	if reg == nil {
		t.Fatal("LoadRegistry() with empty JSON returned nil registry")
	}
	if len(reg.Operators()) != 0 {
		t.Errorf("len(Operators()) = %d, want 0", len(reg.Operators()))
	}
}

func TestLoadRegistry_SkipsInvalidOperator(t *testing.T) {
	entries := []registryOperatorEntry{
		{
			ID:         "op-valid",
			PayoutNpub: validNPub,
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				ClientIP: "10.0.0.1",
			},
		},
		{
			ID: "op-invalid-no-payout",
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{},
		},
	}

	path := writeRegistryFile(t, entries)
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry() returned error: %v", err)
	}
	ops := reg.Operators()
	if len(ops) != 1 {
		t.Fatalf("len(Operators()) = %d, want 1 (invalid operator skipped)", len(ops))
	}
	if ops[0].Account.ID != "op-valid" {
		t.Errorf("ops[0].Account.ID = %q, want %q", ops[0].Account.ID, "op-valid")
	}
}

func TestResolve_ByClientIP(t *testing.T) {
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

	path := writeRegistryFile(t, entries)
	reg, _ := LoadRegistry(path)

	ctx := reg.Resolve("192.168.1.100", "")
	if ctx == nil {
		t.Fatal("Resolve() returned nil, expected non-nil")
	}
	if ctx.Account.ID != "op-cafe" {
		t.Errorf("Account.ID = %q, want %q", ctx.Account.ID, "op-cafe")
	}
	if !ctx.Resolved {
		t.Error("Resolved = false, want true")
	}
}

func TestResolve_ByNASID(t *testing.T) {
	entries := []registryOperatorEntry{
		{
			ID:         "op-hotel",
			PayoutNpub: validNPub,
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				NASID: "hotel-floor-3",
			},
		},
	}

	path := writeRegistryFile(t, entries)
	reg, _ := LoadRegistry(path)

	// Case-insensitive match
	ctx := reg.Resolve("", "HOTEL-FLOOR-3")
	if ctx == nil {
		t.Fatal("Resolve() returned nil for case-insensitive NASID")
	}
	if ctx.Account.ID != "op-hotel" {
		t.Errorf("Account.ID = %q, want %q", ctx.Account.ID, "op-hotel")
	}
}

func TestResolve_ClientIPPrioritizedOverNASID(t *testing.T) {
	entries := []registryOperatorEntry{
		{
			ID:         "op-by-nasid",
			PayoutNpub: "npub1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				NASID: "ap-matching",
			},
		},
		{
			ID:         "op-by-ip",
			PayoutNpub: "npub1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				ClientIP: "10.0.0.5",
			},
		},
	}

	path := writeRegistryFile(t, entries)
	reg, _ := LoadRegistry(path)

	ctx := reg.Resolve("10.0.0.5", "ap-matching")
	if ctx == nil {
		t.Fatal("Resolve() returned nil")
	}
	if ctx.Account.ID != "op-by-ip" {
		t.Errorf("Account.ID = %q, want %q (client_ip should win over nas_id)", ctx.Account.ID, "op-by-ip")
	}
}

func TestResolve_DefaultOperator(t *testing.T) {
	entries := []registryOperatorEntry{
		{
			ID:         "op-cafe",
			PayoutNpub: "npub1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				ClientIP: "192.168.1.1",
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

	path := writeRegistryFile(t, entries)
	reg, _ := LoadRegistry(path)

	ctx := reg.Resolve("unknown-ip", "unknown-nas")
	if ctx == nil {
		t.Fatal("Resolve() returned nil, expected default operator")
	}
	if ctx.Account.ID != "op-default" {
		t.Errorf("Account.ID = %q, want %q (default fallback)", ctx.Account.ID, "op-default")
	}
}

func TestResolve_NoMatchNoDefault(t *testing.T) {
	entries := []registryOperatorEntry{
		{
			ID:         "op-cafe",
			PayoutNpub: validNPub,
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				ClientIP: "192.168.1.1",
			},
		},
	}

	path := writeRegistryFile(t, entries)
	reg, _ := LoadRegistry(path)

	ctx := reg.Resolve("unknown-ip", "unknown-nas")
	if ctx != nil {
		t.Errorf("Resolve() = %+v, want nil (no match, no default)", ctx)
	}
}

func TestResolve_EmptyRegistry(t *testing.T) {
	reg := &OperatorRegistry{}

	ctx := reg.Resolve("1.2.3.4", "some-id")
	if ctx != nil {
		t.Errorf("Resolve() on empty registry = %+v, want nil", ctx)
	}
}

func TestOperators(t *testing.T) {
	entries := []registryOperatorEntry{
		{
			ID:         "op-1",
			PayoutNpub: "npub1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{},
		},
		{
			ID:         "op-2",
			PayoutNpub: "npub1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{
				Default: true,
			},
		},
		{
			ID:          "op-3",
			PayoutLNURL: "lnurl1dp68gurn8ghj7ampd3kx2ar0veekzar0wd5xjtnrdakj7",
			Match: struct {
				ClientIP string `json:"client_ip"`
				NASID    string `json:"nas_id"`
				Default  bool   `json:"default"`
			}{},
		},
	}

	path := writeRegistryFile(t, entries)
	reg, _ := LoadRegistry(path)

	ops := reg.Operators()
	if len(ops) != 3 {
		t.Fatalf("len(Operators()) = %d, want 3", len(ops))
	}

	wantIDs := []string{"op-1", "op-2", "op-3"}
	for i, want := range wantIDs {
		if ops[i].Account.ID != want {
			t.Errorf("ops[%d].Account.ID = %q, want %q", i, ops[i].Account.ID, want)
		}
	}
}
