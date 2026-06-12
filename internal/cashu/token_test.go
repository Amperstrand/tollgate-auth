package cashu

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// --- Helpers for constructing test tokens ---

// encodeV3Token creates a cashuA-prefixed V3 token from the given V3Token struct.
func encodeV3Token(v3 V3Token) string {
	jsonBytes, _ := json.Marshal(v3)
	// Use raw base64url encoding (no padding) like the Cashu spec
	encoded := base64.RawURLEncoding.EncodeToString(jsonBytes)
	return "cashuA" + encoded
}

// encodeV4Token creates a cashuB-prefixed V4 token from the given V4Token struct.
func encodeV4Token(v4 V4Token) string {
	cborBytes, _ := cbor.Marshal(v4)
	encoded := base64.RawURLEncoding.EncodeToString(cborBytes)
	return "cashuB" + encoded
}

// --- DecodeToken ---

func TestDecodeToken_RejectsEmptyString(t *testing.T) {
	_, err := DecodeToken("")
	if err == nil {
		t.Fatal("expected error for empty string")
	}
}

func TestDecodeToken_RejectsInvalidPrefix(t *testing.T) {
	cases := []string{
		"cashuXabc",
		"CASHUAabc",
		"somethingelse",
		"cashu",
		"cashuAbutnotvalidbase64!!!",
	}
	for _, tc := range cases {
		_, err := DecodeToken(tc)
		if err == nil {
			t.Errorf("expected error for input %q", tc)
		}
	}
}

func TestDecodeToken_RejectsTooShortToken(t *testing.T) {
	_, err := DecodeToken("cashuA")
	if err == nil {
		t.Fatal("expected error for cashuA with no payload")
	}
	_, err = DecodeToken("cashuB")
	if err == nil {
		t.Fatal("expected error for cashuB with no payload")
	}
}

func TestDecodeV3_BasicToken(t *testing.T) {
	token := encodeV3Token(V3Token{
		Token: []V3Mint{
			{
				Mint: "https://testnut.cashu.space",
				Proofs: []V3Proof{
					{Amount: 8, ID: "key1", Secret: "secret1", C: "02abcdef"},
				},
			},
		},
		Unit: "sat",
		Memo: "test token",
	})

	td, err := DecodeToken(token)
	if err != nil {
		t.Fatalf("DecodeToken error: %v", err)
	}
	if td.Mint != "https://testnut.cashu.space" {
		t.Errorf("mint = %q, want https://testnut.cashu.space", td.Mint)
	}
	if td.Amount != 8 {
		t.Errorf("amount = %d, want 8", td.Amount)
	}
	if td.Unit != "sat" {
		t.Errorf("unit = %q, want sat", td.Unit)
	}
	if td.Memo != "test token" {
		t.Errorf("memo = %q, want 'test token'", td.Memo)
	}
	if len(td.Proofs) != 1 {
		t.Fatalf("proofs count = %d, want 1", len(td.Proofs))
	}
	if td.Proofs[0].Secret != "secret1" {
		t.Errorf("proof secret = %q, want secret1", td.Proofs[0].Secret)
	}
}

func TestDecodeV3_MultipleProofs(t *testing.T) {
	token := encodeV3Token(V3Token{
		Token: []V3Mint{
			{
				Mint: "https://testnut.cashu.space",
				Proofs: []V3Proof{
					{Amount: 4, ID: "k1", Secret: "s1", C: "c1"},
					{Amount: 4, ID: "k2", Secret: "s2", C: "c2"},
				},
			},
		},
		Unit: "sat",
	})

	td, err := DecodeToken(token)
	if err != nil {
		t.Fatalf("DecodeToken error: %v", err)
	}
	if td.Amount != 8 {
		t.Errorf("amount = %d, want 8 (4+4)", td.Amount)
	}
	if len(td.Proofs) != 2 {
		t.Errorf("proofs count = %d, want 2", len(td.Proofs))
	}
}

func TestDecodeV3_ZeroValueToken(t *testing.T) {
	token := encodeV3Token(V3Token{
		Token: []V3Mint{
			{
				Mint:   "https://testnut.cashu.space",
				Proofs: []V3Proof{},
			},
		},
		Unit: "sat",
	})

	_, err := DecodeToken(token)
	if err == nil {
		t.Fatal("expected error for zero-value V3 token")
	}
}

func TestDecodeV3_NoMintURL(t *testing.T) {
	token := encodeV3Token(V3Token{
		Token: []V3Mint{
			{
				Mint: "",
				Proofs: []V3Proof{
					{Amount: 1, ID: "k1", Secret: "s1", C: "c1"},
				},
			},
		},
		Unit: "sat",
	})

	_, err := DecodeToken(token)
	if err == nil {
		t.Fatal("expected error for V3 token with no mint URL")
	}
}

func TestDecodeV4_BasicToken(t *testing.T) {
	keysetID, _ := hex.DecodeString("00deadbeef")
	cBytes, _ := hex.DecodeString("02abcdef")

	token := encodeV4Token(V4Token{
		Mint: "https://testnut.cashu.space",
		Unit: "sat",
		Memo: "v4 test",
		Token: []V4Entry{
			{
				KeysetID: keysetID,
				Proofs: []V4Proof{
					{Amount: 16, Secret: "v4secret", C: cBytes},
				},
			},
		},
	})

	td, err := DecodeToken(token)
	if err != nil {
		t.Fatalf("DecodeToken error: %v", err)
	}
	if td.Mint != "https://testnut.cashu.space" {
		t.Errorf("mint = %q, want https://testnut.cashu.space", td.Mint)
	}
	if td.Amount != 16 {
		t.Errorf("amount = %d, want 16", td.Amount)
	}
	if td.Unit != "sat" {
		t.Errorf("unit = %q, want sat", td.Unit)
	}
	if td.Memo != "v4 test" {
		t.Errorf("memo = %q, want 'v4 test'", td.Memo)
	}
	if len(td.Proofs) != 1 {
		t.Fatalf("proofs count = %d, want 1", len(td.Proofs))
	}
	if td.Proofs[0].Secret != "v4secret" {
		t.Errorf("proof secret = %q, want v4secret", td.Proofs[0].Secret)
	}
	// ID should be hex of keysetID
	expectedID := fmt.Sprintf("%x", keysetID)
	if td.Proofs[0].ID != expectedID {
		t.Errorf("proof ID = %q, want %q", td.Proofs[0].ID, expectedID)
	}
}

func TestDecodeV4_ZeroValueToken(t *testing.T) {
	token := encodeV4Token(V4Token{
		Mint:  "https://testnut.cashu.space",
		Unit:  "sat",
		Token: []V4Entry{}, // no proofs
	})

	_, err := DecodeToken(token)
	if err == nil {
		t.Fatal("expected error for zero-value V4 token")
	}
}

func TestDecodeV4_NoMintURL(t *testing.T) {
	cBytes, _ := hex.DecodeString("02ab")
	token := encodeV4Token(V4Token{
		Mint: "",
		Unit: "sat",
		Token: []V4Entry{
			{
				KeysetID: []byte{0x00},
				Proofs: []V4Proof{
					{Amount: 1, Secret: "s", C: cBytes},
				},
			},
		},
	})

	_, err := DecodeToken(token)
	if err == nil {
		t.Fatal("expected error for V4 token with no mint URL")
	}
}

func TestDecodeV4_InvalidBase64(t *testing.T) {
	_, err := DecodeToken("cashuB!!!not-base64!!!")
	if err == nil {
		t.Fatal("expected error for invalid V4 base64")
	}
}

func TestDecodeV4_InvalidCBOR(t *testing.T) {
	// Valid base64 but not valid CBOR
	encoded := base64.RawURLEncoding.EncodeToString([]byte("not cbor data"))
	_, err := DecodeToken("cashuB" + encoded)
	if err == nil {
		t.Fatal("expected error for invalid CBOR payload")
	}
}

func TestDecodeV3_InvalidBase64(t *testing.T) {
	_, err := DecodeToken("cashuA!!!not-base64!!!")
	if err == nil {
		t.Fatal("expected error for invalid V3 base64")
	}
}

func TestDecodeV3_InvalidJSON(t *testing.T) {
	// Valid base64 but not valid JSON structure
	encoded := base64.RawURLEncoding.EncodeToString([]byte(`{"not":"a token"}`))
	_, err := DecodeToken("cashuA" + encoded)
	// Should succeed or fail gracefully — the token would have 0 proofs → zero value error
	// Actually the JSON will decode but have empty proofs, so zero value
	if err == nil {
		t.Fatal("expected error for V3 token with no proofs (zero value)")
	}
}

func TestDecodeV4_MultipleEntriesAndProofs(t *testing.T) {
	keyset1 := []byte{0x01, 0x02}
	keyset2 := []byte{0x03, 0x04}
	c1, _ := hex.DecodeString("02aa")
	c2, _ := hex.DecodeString("02bb")
	c3, _ := hex.DecodeString("02cc")

	token := encodeV4Token(V4Token{
		Mint: "https://testnut.cashu.space",
		Unit: "sat",
		Token: []V4Entry{
			{
				KeysetID: keyset1,
				Proofs: []V4Proof{
					{Amount: 2, Secret: "s1", C: c1},
					{Amount: 4, Secret: "s2", C: c2},
				},
			},
			{
				KeysetID: keyset2,
				Proofs: []V4Proof{
					{Amount: 8, Secret: "s3", C: c3},
				},
			},
		},
	})

	td, err := DecodeToken(token)
	if err != nil {
		t.Fatalf("DecodeToken error: %v", err)
	}
	if td.Amount != 14 {
		t.Errorf("amount = %d, want 14 (2+4+8)", td.Amount)
	}
	if len(td.Proofs) != 3 {
		t.Errorf("proofs count = %d, want 3", len(td.Proofs))
	}
}

// --- TokenHash ---

func TestTokenHash_KnownValue(t *testing.T) {
	// SHA256 of "hello" is well-known
	h := sha256.Sum256([]byte("hello"))
	expected := fmt.Sprintf("%x", h)
	got := TokenHash("hello")
	if got != expected {
		t.Errorf("TokenHash(\"hello\") = %q, want %q", got, expected)
	}
}

func TestTokenHash_Deterministic(t *testing.T) {
	h1 := TokenHash("cashuAabc123")
	h2 := TokenHash("cashuAabc123")
	if h1 != h2 {
		t.Errorf("TokenHash not deterministic: %q vs %q", h1, h2)
	}
}

func TestTokenHash_DifferentInputs(t *testing.T) {
	h1 := TokenHash("cashuAabc")
	h2 := TokenHash("cashuAdef")
	if h1 == h2 {
		t.Error("different inputs should produce different hashes")
	}
}

func TestTokenHash_EmptyString(t *testing.T) {
	h := TokenHash("")
	if len(h) != 64 {
		t.Errorf("hash length = %d, want 64 hex chars", len(h))
	}
}

func TestTokenHash_Length(t *testing.T) {
	h := TokenHash("any input")
	if len(h) != 64 {
		t.Errorf("SHA256 hex length = %d, want 64", len(h))
	}
}
