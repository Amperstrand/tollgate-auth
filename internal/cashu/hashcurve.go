package cashu

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
)

// HashToCurve computes Y = hash_to_curve(secret) per NUT-12.
//
// Algorithm matches the cdk-mintd reference (cashubtc/cdk) which most
// production mints run: Y is found by iterating SHA-256(secret || counter_be32)
// until the resulting x maps to a valid secp256k1 point with even y.
//
// The Python cashu wallet uses a slightly different algorithm (chained
// SHA-256 without a counter) — tokens minted by python-cashu wallets against
// non-cdk mints may compute Y differently. Test mints like testnut.cashu.space
// run cdk-mintd and accept the algorithm here.
//
// We expose this function so callers can build correct NUT-07 /v1/checkstate
// requests: the mint looks up proof state keyed by Y, not by the raw secret.
func HashToCurve(secret string) ([]byte, error) {
	secretBytes := []byte(secret)
	for counter := uint32(0); counter < 65536; counter++ {
		h := sha256.New()
		h.Write(secretBytes)
		var counterBytes [4]byte
		binary.BigEndian.PutUint32(counterBytes[:], counter)
		h.Write(counterBytes[:])
		xHash := h.Sum(nil)

		compressed := make([]byte, 33)
		compressed[0] = 0x02 // even-y parity
		copy(compressed[1:], xHash)

		pk, err := btcec.ParsePubKey(compressed)
		if err != nil {
			continue
		}
		return pk.SerializeCompressed(), nil
	}
	return nil, fmt.Errorf("hash_to_curve: did not converge for secret %q", secret)
}

// errHashToCurveFailed is returned if HashToCurve cannot produce a Y.
var errHashToCurveFailed = errors.New("hash_to_curve failed")
