package cashu

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
)

// domainSeparator is the NUT-00 hash_to_curve domain separator, the constant
// byte string b"Secp256k1_HashToCurve_Cashu_".
const domainSeparator = "Secp256k1_HashToCurve_Cashu_"

// HashToCurve computes Y = hash_to_curve(secret) per NUT-00.
//
//	Y = PublicKey('02' || SHA256(msg_hash || counter))
//	msg_hash = SHA256(DOMAIN_SEPARATOR || x)
//
// The counter is a uint32 in LITTLE-endian byte order, incremented from 0
// until the resulting x maps to a valid secp256k1 point. This matches the
// reference implementations (cashubtc/cdk dhke.rs, cashu-ts core.ts) that
// production mints run, so locally derived Ys match the mint's Y-keyed
// state.
//
// Exposed so callers build correct NUT-07 /v1/checkstate requests: the mint
// looks up proof state keyed by Y, not by the raw secret.
func HashToCurve(secret string) ([]byte, error) {
	// Domain-separator pre-hash: SHA256("Secp256k1_HashToCurve_Cashu_" || x).
	msgHash := sha256.New()
	msgHash.Write([]byte(domainSeparator))
	msgHash.Write([]byte(secret))
	msgHashBytes := msgHash.Sum(nil)

	for counter := uint32(0); counter < 65536; counter++ {
		h := sha256.New()
		h.Write(msgHashBytes)
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
