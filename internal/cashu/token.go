package cashu

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fxamacker/cbor/v2"
)

// --- Token Types ---

type TokenData struct {
	Mint   string       `json:"mint"`
	Unit   string       `json:"unit"`
	Memo   string       `json:"memo"`
	Amount int          `json:"amount"`
	Proofs []ProofEntry `json:"proofs"`
}

type ProofEntry struct {
	Amount int    `json:"amount"`
	ID     string `json:"id"`
	Secret string `json:"secret"`
	C      string `json:"C"`
}

// V4 CBOR types
type V4Token struct {
	Mint  string    `cbor:"m"`
	Unit  string    `cbor:"u"`
	Memo  string    `cbor:"d"`
	Token []V4Entry `cbor:"t"`
}

type V4Entry struct {
	KeysetID []byte    `cbor:"i"`
	Proofs   []V4Proof `cbor:"p"`
}

type V4Proof struct {
	Amount int    `cbor:"a"`
	Secret string `cbor:"s"`
	C      []byte `cbor:"c"`
}

// V3 JSON types
type V3Token struct {
	Token []V3Mint `json:"token"`
	Unit  string   `json:"unit"`
	Memo  string   `json:"memo"`
}

type V3Mint struct {
	Mint   string    `json:"mint"`
	Proofs []V3Proof `json:"proofs"`
}

type V3Proof struct {
	Amount int    `json:"amount"`
	ID     string `json:"id"`
	Secret string `json:"secret"`
	C      string `json:"C"`
}

// Mint checkstate types
type CheckStateRequest struct {
	Proofs []struct {
		Secret string `json:"secret"`
	} `json:"proofs"`
}

type CheckStateResponse struct {
	States []struct {
		State string `json:"state"`
	} `json:"states"`
}

// --- Token Decoding ---

func b64urlDecode(s string) ([]byte, error) {
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.StdEncoding.DecodeString(s)
}

// DecodeToken parses a Cashu token string (cashuA or cashuB prefix)
// and returns the decoded token data.
func DecodeToken(tokenStr string) (*TokenData, error) {
	if strings.HasPrefix(tokenStr, "cashuB") {
		return decodeV4(tokenStr[6:])
	} else if strings.HasPrefix(tokenStr, "cashuA") {
		return decodeV3(tokenStr[6:])
	}
	return nil, fmt.Errorf("not a Cashu token (must start with cashuA or cashuB)")
}

func decodeV4(raw string) (*TokenData, error) {
	cborBytes, err := b64urlDecode(raw)
	if err != nil {
		return nil, fmt.Errorf("V4 base64 decode error: %w", err)
	}

	var v4 V4Token
	if err := cbor.Unmarshal(cborBytes, &v4); err != nil {
		return nil, fmt.Errorf("V4 CBOR decode error: %w", err)
	}

	if v4.Mint == "" {
		return nil, fmt.Errorf("token has no mint URL")
	}

	var proofs []ProofEntry
	amount := 0
	for _, entry := range v4.Token {
		for _, p := range entry.Proofs {
			amount += p.Amount
			proofs = append(proofs, ProofEntry{
				Amount: p.Amount,
				ID:     fmt.Sprintf("%x", entry.KeysetID),
				Secret: p.Secret,
				C:      fmt.Sprintf("%x", p.C),
			})
		}
	}

	if amount == 0 {
		return nil, fmt.Errorf("token has zero value")
	}

	return &TokenData{
		Mint:   v4.Mint,
		Unit:   v4.Unit,
		Memo:   v4.Memo,
		Amount: amount,
		Proofs: proofs,
	}, nil
}

func decodeV3(raw string) (*TokenData, error) {
	jsonBytes, err := b64urlDecode(raw)
	if err != nil {
		return nil, fmt.Errorf("V3 base64 decode error: %w", err)
	}

	var v3 V3Token
	if err := json.Unmarshal(jsonBytes, &v3); err != nil {
		return nil, fmt.Errorf("V3 JSON decode error: %w", err)
	}

	var proofs []ProofEntry
	amount := 0
	mint := ""
	for _, t := range v3.Token {
		if t.Mint != "" {
			mint = t.Mint
		}
		for _, p := range t.Proofs {
			amount += p.Amount
			proofs = append(proofs, ProofEntry{
				Amount: p.Amount,
				ID:     p.ID,
				Secret: p.Secret,
				C:      p.C,
			})
		}
	}

	if amount == 0 {
		return nil, fmt.Errorf("token has zero value")
	}
	if mint == "" {
		return nil, fmt.Errorf("token has no mint URL")
	}

	return &TokenData{
		Mint:   mint,
		Unit:   v3.Unit,
		Memo:   v3.Memo,
		Amount: amount,
		Proofs: proofs,
	}, nil
}

// TokenHash returns the SHA256 hex of a token string.
func TokenHash(tokenStr string) string {
	h := sha256.Sum256([]byte(tokenStr))
	return fmt.Sprintf("%x", h)
}
