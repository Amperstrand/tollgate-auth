package cashu

import (
	"encoding/hex"
	"testing"
)

// TestHashToCurve_NUT00Vectors pins HashToCurve to the byte-exact NUT-00
// construction, using the test vectors from the CDK reference implementation
// (cashubtc/cdk, crates/cashu/src/dhke.rs::test_hash_to_curve).
//
// NUT-00: Y = PublicKey('02' || SHA256(msg_hash || counter_le32)) where
// msg_hash = SHA256("Secp256k1_HashToCurve_Cashu_" || x) and the counter is a
// little-endian uint32 incremented from 0 until x lands on the curve.
// The third vector exercises the multi-iteration path.
func TestHashToCurve_NUT00Vectors(t *testing.T) {
	vectors := []struct {
		name      string
		secretHex string
		expectedY string
		multiIter bool
	}{
		{
			name:      "zero secret",
			secretHex: "0000000000000000000000000000000000000000000000000000000000000000",
			expectedY: "024cce997d3b518f739663b757deaec95bcd9473c30a14ac2fd04023a739d1a725",
		},
		{
			name:      "one",
			secretHex: "0000000000000000000000000000000000000000000000000000000000000001",
			expectedY: "022e7158e11c9506f1aa4248bf531298daa7febd6194f003edcd9b93ade6253acf",
		},
		{
			name:      "two (requires several counter iterations)",
			secretHex: "0000000000000000000000000000000000000000000000000000000000000002",
			expectedY: "026cdbe15362df59cd1dd3c9c11de8aedac2106eca69236ecd9fbe117af897be4f",
			multiIter: true,
		},
	}

	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			secretBytes, err := hex.DecodeString(v.secretHex)
			if err != nil {
				t.Fatalf("decode secret: %v", err)
			}
			got, err := HashToCurve(string(secretBytes))
			if err != nil {
				t.Fatalf("HashToCurve: %v", err)
			}
			if gotHex := hex.EncodeToString(got); gotHex != v.expectedY {
				t.Fatalf("Y mismatch against NUT-00/CDK vector:\n got  %s\n want %s", gotHex, v.expectedY)
			}
		})
	}
}
