package cashu

import (
	"bytes"
	"encoding/hex"
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

// Differential corpus for the #35 NUT-00 sweep. Vectors 0-2 are cdk v0.18.x
// dhke.rs test_hash_to_curve (vector 2 exercises the counter loop); the rest
// were produced by an independent implementation derived from the NUT-00 spec
// text alone (SHA256 + secp256k1 x-coordinate QR check), which reproduces all
// three cdk vectors byte-exact.
var hashToCurveCorpus = []struct {
	name     string
	secret   string
	expected string
}{
	{"cdk zero secret", string(bytes.Repeat([]byte{0x00}, 32)), "024cce997d3b518f739663b757deaec95bcd9473c30a14ac2fd04023a739d1a725"},
	{"cdk ...01", string(append(bytes.Repeat([]byte{0x00}, 31), 0x01)), "022e7158e11c9506f1aa4248bf531298daa7febd6194f003edcd9b93ade6253acf"},
	{"cdk ...02 counter loop", string(append(bytes.Repeat([]byte{0x00}, 31), 0x02)), "026cdbe15362df59cd1dd3c9c11de8aedac2106eca69236ecd9fbe117af897be4f"},
	{"empty secret", "", "0204f5901f3e54cb4fd76bee23c83ca4f965b7009b74b3572f455ab90d88e6cbfe"},
	{"single byte a", "a", "029794c59a5d9b910a18e50e10623c864b77c7edf4552f8652b0c85d30ac0498f0"},
	{"repo test secret", "test_secret_12345", "027450e9cc4316dc28d924532bac07a4a8bde96b56fda4e5d68acb8f8f287afe7b"},
	{"cdk token secret", string(mustHex("407915bc212be61a77e3e6d2aeb4c727980bda51cd06a6afc29e2861768a7837")), "02a2f1607b39d96ae0a3e8292eb841c16f479c659d6019de1c66c94b8e56bfb118"},
	{"utf8 secret", "tollgate-conformance-sweep-35", "02d91347aa8169035e3012739b322913dbc0071045bfebadc16c8a7e489363d256"},
	{"64 byte secret", string(mustHex("407915bc212be61a77e3e6d2aeb4c727980bda51cd06a6afc29e2861768a7837407915bc212be61a77e3e6d2aeb4c727980bda51cd06a6afc29e2861768a7837")), "029d689d1a1cd3d38097bf0d342a5a7adcca9de070b29f03d62d03218f819e7343"},
	{"counter heavy 0xff", string(append(bytes.Repeat([]byte{0x00}, 31), 0xff)), "02a2255c50d16a1728381b83ddbcd28096d9ce3f88cb8496c82360ceb653e9cda5"},
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func TestHashToCurve_DifferentialCorpus(t *testing.T) {
	for _, v := range hashToCurveCorpus {
		t.Run(v.name, func(t *testing.T) {
			y, err := HashToCurve(v.secret)
			if err != nil {
				t.Fatalf("HashToCurve: %v", err)
			}
			got := fmt.Sprintf("%x", y)
			if got != v.expected {
				t.Errorf("hash_to_curve mismatch\nsecret: %q\ngot:    %s\nwant:   %s", v.secret, got, v.expected)
			}
		})
	}
}
