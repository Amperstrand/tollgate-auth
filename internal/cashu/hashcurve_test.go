package cashu

import (
	"bytes"
	"fmt"
	"testing"
)

// NUT-00 hash_to_curve test vectors (from cashu-ts test/crypto/core.test.ts,
// "testing hash to curve"; independently re-derived against the NUT-00 spec:
// msg_hash = SHA256("Secp256k1_HashToCurve_Cashu_" || x), then
// Y = PublicKey('02' || SHA256(msg_hash || counter_LE32)) from counter = 0).
var nut00HashToCurveVectors = []struct {
	name     string
	secret   []byte
	expected string // compressed hex, even-y
}{
	{
		name:     "zero secret",
		secret:   bytes.Repeat([]byte{0x00}, 32),
		expected: "024cce997d3b518f739663b757deaec95bcd9473c30a14ac2fd04023a739d1a725",
	},
	{
		name:     "31 zero bytes then 0x01",
		secret:   append(bytes.Repeat([]byte{0x00}, 31), 0x01),
		expected: "022e7158e11c9506f1aa4248bf531298daa7febd6194f003edcd9b93ade6253acf",
	},
}

func TestHashToCurve_NUT00Vectors(t *testing.T) {
	for _, v := range nut00HashToCurveVectors {
		t.Run(v.name, func(t *testing.T) {
			y, err := HashToCurve(string(v.secret))
			if err != nil {
				t.Fatalf("HashToCurve: %v", err)
			}
			got := fmt.Sprintf("%x", y)
			if got != v.expected {
				t.Errorf("hash_to_curve mismatch\nsecret: %x\ngot:    %s\nwant:   %s", v.secret, got, v.expected)
			}
		})
	}
}
