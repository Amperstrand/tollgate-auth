package cashu

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- VerifyWithMint ---

func TestVerifyWithMint_RealMint_VerifiesViaHTTP(t *testing.T) {
	td := &TokenData{
		Mint:   "https://real-mint.example.com",
		Proofs: []ProofEntry{{Amount: 8, Secret: "s1"}},
	}

	ok, msg := VerifyWithMint(td)
	if ok {
		t.Errorf("real mint should be verified via HTTP (will fail as unreachable), got ok=true msg=%q", msg)
	}
}

func TestVerifyWithMint_RealMintWithServer_SSRFProtected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"states":[{"Y":"02100b2c1b0f3a4d5e6f708192a3b4c5d6e7f809192a3b4c5d6e7f809192a3b4c5d","state":"UNSPENT","witness":null}]}`))
	}))
	defer server.Close()

	td := &TokenData{
		Mint:   server.URL,
		Proofs: []ProofEntry{{Amount: 8, Secret: "test_secret_12345", C: "02100b2c1b0f3a4d5e6f708192a3b4c5d6e7f809192a3b4c5d6e7f809192a3b4c5d"}},
	}

	ok, _ := VerifyWithMint(td)
	if ok {
		t.Error("SSRF protection should block localhost test server")
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

	ok, msg := VerifyWithMint(td)
	if ok {
		t.Errorf("expected ok=false for unreachable mint with trailing slash, got msg=%q", msg)
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

// Regression guard for the malformed-checkstate fix: NUT-07 requires {"Ys":[...]}
// not {"proofs":[{"secret":...}]}.
func TestCheckStateRequest_Serialization(t *testing.T) {
	req := CheckStateRequest{
		Ys: []string{"02599b9ea0a1ad4143706c2a5a4a568ce442dd4313e1cf1f7f0b58a317c1a355ee"},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	var parsed struct {
		Ys     []string       `json:"Ys"`
		Proofs json.RawMessage `json:"proofs"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Ys) != 1 || parsed.Ys[0] != "02599b9ea0a1ad4143706c2a5a4a568ce442dd4313e1cf1f7f0b58a317c1a355ee" {
		t.Errorf("Ys = %v, want single Y hex", parsed.Ys)
	}
	if parsed.Proofs != nil {
		t.Errorf("expected no proofs field (must use Ys per NUT-07), got %s", string(parsed.Proofs))
	}
}

func TestHashToCurve_ProducesCompressedPubkey(t *testing.T) {
	y, err := HashToCurve("test_secret")
	if err != nil {
		t.Fatalf("HashToCurve: %v", err)
	}
	if len(y) != 33 {
		t.Fatalf("expected 33-byte compressed pubkey, got %d", len(y))
	}
	if y[0] != 0x02 && y[0] != 0x03 {
		t.Fatalf("first byte = %#x, want 0x02 or 0x03", y[0])
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
