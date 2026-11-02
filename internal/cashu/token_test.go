package cashu

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
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

// --- NUT-00 conformance sweep (#35) ---

// NUT-00 V3 tokens carry proofs grouped per mint. cdk refuses V3 tokens with
// more than one distinct mint (cashu/src/nuts/nut00/token.rs mint_url():
// "ensure_cdk!(mint_urls.len() == 1, Error::UnsupportedToken)"): the proofs
// cannot be state-checked or redeemed against a single mint. Our previous
// decode took the last non-empty mint and silently attributed ALL proofs to
// it, keying NUT-07 checkstate at the wrong mint.
func TestDecodeV3_MultiMintTokenRejected(t *testing.T) {
	proof := V3Proof{Amount: 4, ID: "009a1f293253e41e", Secret: "s", C: "02bc90"}
	token := encodeV3Token(V3Token{
		Token: []V3Mint{
			{Mint: "https://mint-a.example.com", Proofs: []V3Proof{proof}},
			{Mint: "https://mint-b.example.com", Proofs: []V3Proof{proof}},
		},
		Unit: "sat",
	})

	_, err := DecodeToken(token)
	if err == nil {
		t.Fatal("multi-mint V3 token must be rejected (wrong-mint checkstate keying)")
	}
	if !strings.Contains(err.Error(), "multi-mint") {
		t.Errorf("err = %v, want it to name the multi-mint rejection", err)
	}
}

// Same mint repeated across entries (no padding, mixed) is NOT multi-mint and
// must still decode.
func TestDecodeV3_SameMintTwiceStillDecodes(t *testing.T) {
	proof := V3Proof{Amount: 4, ID: "009a1f293253e41e", Secret: "s", C: "02bc90"}
	token := encodeV3Token(V3Token{
		Token: []V3Mint{
			{Mint: "https://mint-a.example.com", Proofs: []V3Proof{proof}},
			{Mint: "https://mint-a.example.com", Proofs: []V3Proof{proof}},
		},
		Unit: "sat",
	})

	td, err := DecodeToken(token)
	if err != nil {
		t.Fatalf("same-mint V3 token must decode: %v", err)
	}
	if td.Mint != "https://mint-a.example.com" {
		t.Errorf("mint = %q, want https://mint-a.example.com", td.Mint)
	}
	if td.Amount != 8 || len(td.Proofs) != 2 {
		t.Errorf("amount = %d proofs = %d, want 8 / 2", td.Amount, len(td.Proofs))
	}
}

// cdk differential vectors from cashubtc/cdk v0.18.x
// crates/cashu/src/nuts/nut00/token.rs tests (test_token_padding,
// test_token_v4_str_round_trip, test_token_accessors_preserve_v3_and_v4_metadata,
// test_token_proofs_extract_v3_and_v4_proofs, test_token_v4_multi_keyset).
// Expected values are cdk's own assertions; literals cross-checked with an
// independent Python base64/JSON decode.
const (
	cdkV3TokenPadded = "cashuAeyJ0b2tlbiI6W3sibWludCI6Imh0dHBzOi8vODMzMy5zcGFjZTozMzM4IiwicHJvb2ZzIjpbeyJhbW91bnQiOjIsImlkIjoiMDA5YTFmMjkzMjUzZTQxZSIsInNlY3JldCI6IjQwNzkxNWJjMjEyYmU2MWE3N2UzZTZkMmFlYjRjNzI3OTgwYmRhNTFjZDA2YTZhZmMyOWUyODYxNzY4YTc4MzciLCJDIjoiMDJiYzkwOTc5OTdkODFhZmIyY2M3MzQ2YjVlNDM0NWE5MzQ2YmQyYTUwNmViNzk1ODU5OGE3MmYwY2Y4NTE2M2VhIn0seyJhbW91bnQiOjgsImlkIjoiMDA5YTFmMjkzMjUzZTQxZSIsInNlY3JldCI6ImZlMTUxMDkzMTRlNjFkNzc1NmIwZjhlZTBmMjNhNjI0YWNhYTNmNGUwNDJmNjE0MzNjNzI4YzcwNTdiOTMxYmUiLCJDIjoiMDI5ZThlNTA1MGI4OTBhN2Q2YzA5NjhkYjE2YmMxZDVkNWZhMDQwZWExZGUyODRmNmVjNjlkNjEyOTlmNjcxMDU5In1dfV0sInVuaXQiOiJzYXQiLCJtZW1vIjoiVGhhbmsgeW91IHZlcnkgbXVjaC4ifQ=="

	cdkV3TokenNoPadding = "cashuAeyJ0b2tlbiI6W3sibWludCI6Imh0dHBzOi8vODMzMy5zcGFjZTozMzM4IiwicHJvb2ZzIjpbeyJhbW91bnQiOjIsImlkIjoiMDA5YTFmMjkzMjUzZTQxZSIsInNlY3JldCI6IjQwNzkxNWJjMjEyYmU2MWE3N2UzZTZkMmFlYjRjNzI3OTgwYmRhNTFjZDA2YTZhZmMyOWUyODYxNzY4YTc4MzciLCJDIjoiMDJiYzkwOTc5OTdkODFhZmIyY2M3MzQ2YjVlNDM0NWE5MzQ2YmQyYTUwNmViNzk1ODU5OGE3MmYwY2Y4NTE2M2VhIn0seyJhbW91bnQiOjgsImlkIjoiMDA5YTFmMjkzMjUzZTQxZSIsInNlY3JldCI6ImZlMTUxMDkzMTRlNjFkNzc1NmIwZjhlZTBmMjNhNjI0YWNhYTNmNGUwNDJmNjE0MzNjNzI4YzcwNTdiOTMxYmUiLCJDIjoiMDI5ZThlNTA1MGI4OTBhN2Q2YzA5NjhkYjE2YmMxZDVkNWZhMDQwZWExZGUyODRmNmVjNjlkNjEyOTlmNjcxMDU5In1dfV0sInVuaXQiOiJzYXQiLCJtZW1vIjoiVGhhbmsgeW91LiJ9"

	cdkV4Token = "cashuBpGF0gaJhaUgArSaMTR9YJmFwgaNhYQFhc3hAOWE2ZGJiODQ3YmQyMzJiYTc2ZGIwZGYxOTcyMTZiMjlkM2I4Y2MxNDU1M2NkMjc4MjdmYzFjYzk0MmZlZGI0ZWFjWCEDhhhUP_trhpXfStS6vN6So0qWvc2X3O4NfM-Y1HISZ5JhZGlUaGFuayB5b3VhbXVodHRwOi8vbG9jYWxob3N0OjMzMzhhdWNzYXQ="

	cdkV4TokenMultiKeyset = "cashuBo2F0gqJhaUgA_9SLj17PgGFwgaNhYQFhc3hAYWNjMTI0MzVlN2I4NDg0YzNjZjE4NTAxNDkyMThhZjkwZjcxNmE1MmJmNGE1ZWQzNDdlNDhlY2MxM2Y3NzM4OGFjWCECRFODGd5IXVW-07KaZCvuWHk3WrnnpiDhHki6SCQh88-iYWlIAK0mjE0fWCZhcIKjYWECYXN4QDEzMjNkM2Q0NzA3YTU4YWQyZTIzYWRhNGU5ZjFmNDlmNWE1YjRhYzdiNzA4ZWIwZDYxZjczOGY0ODMwN2U4ZWVhY1ghAjRWqhENhLSsdHrr2Cw7AFrKUL9Ffr1XN6RBT6w659lNo2FhAWFzeEA1NmJjYmNiYjdjYzY0MDZiM2ZhNWQ1N2QyMTc0ZjRlZmY4YjQ0MDJiMTc2OTI2ZDNhNTdkM2MzZGNiYjU5ZDU3YWNYIQJzEpxXGeWZN5qXSmJjY8MzxWyvwObQGr5G1YCCgHicY2FtdWh0dHA6Ly9sb2NhbGhvc3Q6MzMzOGF1Y3NhdA=="
)

// cdk test_token_padding: padded and unpadded base64url encodings must both
// decode (NUT-00 padding is optional). NB the two cdk fixture strings differ
// in memo ("Thank you very much." vs "Thank you."), so each variant is
// checked against its own decoded memo.
func TestDecodeToken_CdkV3PaddingVariants(t *testing.T) {
	proofs := []ProofEntry{
		{
			Amount: 2,
			ID:     "009a1f293253e41e",
			Secret: "407915bc212be61a77e3e6d2aeb4c727980bda51cd06a6afc29e2861768a7837",
			C:      "02bc9097997d81afb2cc7346b5e4345a9346bd2a506eb7958598a72f0cf85163ea",
		},
		{
			Amount: 8,
			ID:     "009a1f293253e41e",
			Secret: "fe15109314e61d7756b0f8ee0f23a624acaa3f4e042f61433c728c7057b931be",
			C:      "029e8e5050b890a7d6c0968db16bc1d5d5fa040ea1de284f6ec69d61299f671059",
		},
	}

	for name, tc := range map[string]struct {
		token string
		memo  string
	}{
		"padded":   {cdkV3TokenPadded, "Thank you very much."},
		"unpadded": {cdkV3TokenNoPadding, "Thank you."},
	} {
		td, err := DecodeToken(tc.token)
		if err != nil {
			t.Fatalf("%s variant decode: %v", name, err)
		}
		if td.Mint != "https://8333.space:3338" {
			t.Errorf("%s: mint = %q, want https://8333.space:3338", name, td.Mint)
		}
		if td.Unit != "sat" || td.Memo != tc.memo || td.Amount != 10 || len(td.Proofs) != 2 {
			t.Errorf("%s: unit=%q memo=%q amount=%d proofs=%d, want sat/%q/10/2",
				name, td.Unit, td.Memo, td.Amount, len(td.Proofs), tc.memo)
			continue
		}
		for i := range proofs {
			if td.Proofs[i] != proofs[i] {
				t.Errorf("%s: proof[%d] = %+v, want %+v", name, i, td.Proofs[i], proofs[i])
			}
		}
	}
}

// cdk test_token_v4_str_round_trip / test_token_accessors: V4 fields m/u/d/t,
// entry i (keyset bytes), proof a/s/c.
func TestDecodeToken_CdkV4Vector(t *testing.T) {
	td, err := DecodeToken(cdkV4Token)
	if err != nil {
		t.Fatalf("V4 decode: %v", err)
	}

	if td.Mint != "http://localhost:3338" {
		t.Errorf("mint = %q, want http://localhost:3338", td.Mint)
	}
	if td.Unit != "sat" {
		t.Errorf("unit = %q, want sat", td.Unit)
	}
	if td.Memo != "Thank you" {
		t.Errorf("memo = %q, want \"Thank you\"", td.Memo)
	}
	if td.Amount != 1 {
		t.Errorf("amount = %d, want 1 (cdk: Amount::ONE)", td.Amount)
	}
	if len(td.Proofs) != 1 {
		t.Fatalf("proofs = %d, want 1", len(td.Proofs))
	}
	if td.Proofs[0].ID != "00ad268c4d1f5826" {
		t.Errorf("keyset id = %q, want 00ad268c4d1f5826", td.Proofs[0].ID)
	}
}

// cdk test_token_v4_multi_keyset: multiple keyset entries under one mint,
// total value 4.
func TestDecodeToken_CdkV4MultiKeysetVector(t *testing.T) {
	td, err := DecodeToken(cdkV4TokenMultiKeyset)
	if err != nil {
		t.Fatalf("V4 multi-keyset decode: %v", err)
	}

	if td.Mint != "http://localhost:3338" {
		t.Errorf("mint = %q, want http://localhost:3338", td.Mint)
	}
	if td.Unit != "sat" {
		t.Errorf("unit = %q, want sat", td.Unit)
	}
	if td.Amount != 4 {
		t.Errorf("amount = %d, want 4 (cdk: Amount::from(4))", td.Amount)
	}
	if len(td.Proofs) != 3 {
		t.Fatalf("proofs = %d, want 3", len(td.Proofs))
	}
	keysets := map[string]bool{}
	for _, p := range td.Proofs {
		keysets[p.ID] = true
	}
	for _, want := range []string{"00ffd48b8f5ecf80", "00ad268c4d1f5826"} {
		if !keysets[want] {
			t.Errorf("keyset %s missing from decoded token (have %v)", want, keysets)
		}
	}
}
