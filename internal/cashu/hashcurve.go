package cashu

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
)

// domainSeparator is the NUT-00 hash_to_curve domain separator
// (b"Secp256k1_HashToCurve_Cashu_", 28 bytes).
const domainSeparator = "Secp256k1_HashToCurve_Cashu_"

// HashToCurve computes Y = hash_to_curve(secret) per NUT-00.
//
// Construction (byte-exact match to the CDK reference implementation,
// cashubtc/cdk crates/cashu/src/dhke.rs, which cdk-mintd mints run):
//
//	msg_hash = SHA256(domainSeparator || secret)
//	for counter = 0..65535 (uint32, little-endian):
//	    digest = SHA256(msg_hash || counter_le32)
//	    Y = compressed pubkey 0x02 || digest   // even-y point; valid x wins
//
// Exposed so callers build correct NUT-07 /v1/checkstate requests: the mint
// looks up proof state keyed by Y, not by the raw secret — a non-NUT-00 Y
// makes spent/pending proofs read as unknown (issue #22).
func HashToCurve(secret string) ([]byte, error) {
	secretBytes := []byte(secret)
	msgHash := sha256.Sum256(append([]byte(domainSeparator), secretBytes...))

	for counter := uint32(0); counter < 65536; counter++ {
		h := sha256.New()
		h.Write(msgHash[:])
		var counterBytes [4]byte
		binary.LittleEndian.PutUint32(counterBytes[:], counter)
		h.Write(counterBytes[:])
		xHash := h.Sum(nil)

		compressed := make([]byte, 33)
		compressed[0] = 0x02
		copy(compressed[1:], xHash)

		pk, err := btcec.ParsePubKey(compressed)
		if err != nil {
			continue
		}
		return pk.SerializeCompressed(), nil
	}
	return nil, fmt.Errorf("hash_to_curve: did not converge for secret %q", secret)
}
