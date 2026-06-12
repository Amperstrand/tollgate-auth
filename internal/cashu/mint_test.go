package cashu

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- VerifyWithMint ---

func TestVerifyWithMint_NonTestMint_SkipsVerification(t *testing.T) {
	td := &TokenData{
		Mint:   "https://real-mint.example.com",
		Proofs: []ProofEntry{{Amount: 8, Secret: "s1"}},
	}

	ok, msg := VerifyWithMint(td)
	if !ok {
		t.Errorf("non-test mint should skip verification, got ok=false msg=%q", msg)
	}
	if msg != "OK" {
		t.Errorf("msg = %q, want OK", msg)
	}
}

func TestVerifyWithMint_NonTestMintWithServer(t *testing.T) {
	// Server should never be called for non-test mints
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	td := &TokenData{
		Mint:   server.URL,
		Proofs: []ProofEntry{{Amount: 8, Secret: "s1"}},
	}

	ok, msg := VerifyWithMint(td)
	if !ok {
		t.Errorf("expected ok=true for non-test mint, got msg=%q", msg)
	}
	if called {
		t.Error("server should not be called for non-test mint")
	}
}

func TestVerifyWithMint_InvalidMintURL_NoHTTP(t *testing.T) {
	td := &TokenData{
		Mint:   "ftp://bad-protocol.example.com",
		Proofs: []ProofEntry{{Amount: 8, Secret: "s1"}},
	}

	ok, msg := VerifyWithMint(td)
	if ok {
		t.Error("expected ok=false for non-http mint URL")
	}
	if msg != "Invalid mint URL" {
		t.Errorf("msg = %q, want 'Invalid mint URL'", msg)
	}
}

func TestVerifyWithMint_TestMint_SSRFRejection(t *testing.T) {
	tests := []struct {
		name string
		mint string
	}{
		{"localhost", "http://localhost/test"},
		{"127.0.0.1", "http://127.0.0.1/test"},
		{"10.x", "http://10.0.0.1/test"},
		{"192.168", "http://192.168.1.1/test"},
		{"172.16", "http://172.16.0.1/test"},
		{"169.254", "http://169.254.1.1/test"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			td := &TokenData{
				Mint:   tc.mint,
				Proofs: []ProofEntry{{Amount: 8, Secret: "s1"}},
			}
			ok, msg := VerifyWithMint(td)
			if ok {
				t.Errorf("expected ok=false for SSRF-blocked URL %q", tc.mint)
			}
			if msg != "Mint URL rejected (SSRF protection)" {
				t.Errorf("msg = %q, want 'Mint URL rejected (SSRF protection)'", msg)
			}
		})
	}
}

func TestVerifyWithMint_TestMint_UnreachablePublic(t *testing.T) {
	td := &TokenData{
		Mint:   "https://test-unreachable-mint.invalid.example.com",
		Proofs: []ProofEntry{{Amount: 8, Secret: "s1"}},
	}

	ok, msg := VerifyWithMint(td)
	if ok {
		t.Error("expected ok=false for unreachable test mint")
	}
	if !strings.Contains(msg, "Mint unreachable") {
		t.Errorf("msg = %q, want 'Mint unreachable'", msg)
	}
}

func TestVerifyWithMint_TrailingSlashStripped(t *testing.T) {
	td := &TokenData{
		Mint:   "https://mint.example.com/",
		Proofs: []ProofEntry{{Amount: 1, Secret: "s"}},
	}

	ok, _ := VerifyWithMint(td)
	if !ok {
		t.Error("expected ok=true for non-test mint with trailing slash")
	}
}

func TestVerifyWithMint_TestMintInPath(t *testing.T) {
	td := &TokenData{
		Mint:   "https://mint.example.com/test-endpoint",
		Proofs: []ProofEntry{{Amount: 8, Secret: "s1"}},
	}

	ok, msg := VerifyWithMint(td)
	if ok {
		t.Error("expected failure for unreachable test mint")
	}
	if !strings.Contains(msg, "Mint unreachable") && !strings.Contains(msg, "Mint error") {
		t.Errorf("msg = %q, expected mint error", msg)
	}
}

func TestVerifyWithMint_TestCaseInsensitiveTestCheck(t *testing.T) {
	td := &TokenData{
		Mint:   "https://TESTMINT.example.com",
		Proofs: []ProofEntry{{Amount: 8, Secret: "s1"}},
	}

	ok, msg := VerifyWithMint(td)
	if ok {
		t.Error("TEST (uppercase) should be detected as test mint and fail (unreachable)")
	}
	if msg == "OK" {
		t.Error("should not return OK for test mint that can't be reached")
	}
}

// --- isSafeMintURL ---

func TestIsSafeMintURL_Table(t *testing.T) {
	tests := []struct {
		url  string
		safe bool
	}{
		{"https://testnut.cashu.space", true},
		{"https://mint.example.com", true},
		{"http://public-host.org", true},
		{"http://localhost/path", false},
		{"https://localhost:8080", false},
		{"http://127.0.0.1", false},
		{"https://127.0.0.1:443", false},
		{"http://[::1]", false},
		{"http://10.0.0.1", false},
		{"http://10.255.255.255", false},
		{"http://192.168.1.1", false},
		{"http://192.168.0.100", false},
		{"http://169.254.1.1", false},
		{"http://172.16.0.1", false},
		{"http://172.31.255.255", false},
		{"http://172.20.5.3", false},
		{"http://172.15.0.1", true},
		{"http://172.32.0.1", true},
		{"http://172.1.0.1", true},
		{"http://172.200.0.1", true},
		{"testnut.cashu.space", false},
		{"ftp://example.com", false},
	}

	for _, tc := range tests {
		got := isSafeMintURL(tc.url)
		if got != tc.safe {
			t.Errorf("isSafeMintURL(%q) = %v, want %v", tc.url, got, tc.safe)
		}
	}
}

func TestIsSafeMintURL_EmptyString(t *testing.T) {
	if isSafeMintURL("") {
		t.Error("empty string should be unsafe")
	}
}

func TestIsSafeMintURL_InvalidURL(t *testing.T) {
	if isSafeMintURL("http://[invalid") {
		t.Error("malformed URL should be unsafe")
	}
}

func TestIsSafeMintURL_Boundary172Range(t *testing.T) {
	if !isSafeMintURL("http://172.15.255.255") {
		t.Error("172.15.x should be safe (below range)")
	}
	if isSafeMintURL("http://172.16.0.0") {
		t.Error("172.16.0.0 should be unsafe (range start)")
	}
	if isSafeMintURL("http://172.31.255.255") {
		t.Error("172.31.255.255 should be unsafe (range end)")
	}
	if !isSafeMintURL("http://172.32.0.0") {
		t.Error("172.32.0.0 should be safe (above range)")
	}
}

// --- CheckState types serialization ---

func TestCheckStateRequest_Serialization(t *testing.T) {
	req := CheckStateRequest{}
	req.Proofs = append(req.Proofs,
		struct{ Secret string `json:"secret"` }{Secret: "secret1"},
		struct{ Secret string `json:"secret"` }{Secret: "secret2"})

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	var parsed struct {
		Proofs []struct {
			Secret string `json:"secret"`
		} `json:"proofs"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Proofs) != 2 {
		t.Fatalf("expected 2 proofs, got %d", len(parsed.Proofs))
	}
	if parsed.Proofs[0].Secret != "secret1" {
		t.Errorf("proof[0].Secret = %q, want secret1", parsed.Proofs[0].Secret)
	}
}

func TestCheckStateResponse_Unspent(t *testing.T) {
	body := `{"states":[{"state":"UNSPENT"},{"state":"UNSPENT"}]}`
	var resp CheckStateResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	for _, s := range resp.States {
		if s.State != "UNSPENT" {
			t.Errorf("state = %q, want UNSPENT", s.State)
		}
	}
}

func TestCheckStateResponse_Spent(t *testing.T) {
	body := `{"states":[{"state":"SPENT"}]}`
	var resp CheckStateResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.States[0].State != "SPENT" {
		t.Errorf("state = %q, want SPENT", resp.States[0].State)
	}
}
