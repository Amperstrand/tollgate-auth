package testtoken

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/fxamacker/cbor/v2"
)

type v4Token struct {
	Mint  string    `cbor:"m"`
	Unit  string    `cbor:"u"`
	Memo  string    `cbor:"d"`
	Token []v4Entry `cbor:"t"`
}

type v4Entry struct {
	KeysetID []byte    `cbor:"i"`
	Proofs   []v4Proof `cbor:"p"`
}

type v4Proof struct {
	Amount int    `cbor:"a"`
	Secret string `cbor:"s"`
	C      []byte `cbor:"c"`
}

func encodeV4(t v4Token) string {
	b, _ := cbor.Marshal(t)
	return "cashuB" + base64.RawURLEncoding.EncodeToString(b)
}

var (
	keysetID = func() []byte {
		b, _ := hex.DecodeString("00deadbeef")
		return b
	}()
	c33 = func() []byte {
		b, _ := hex.DecodeString("02100b2c1b0f3a4d5e6f708192a3b4c5d6e7f809192a3b4c5d6e7f809192a3b4c5d")
		return b
	}()
)

const TestMint = "https://testnut.cashu.space"

func V4Token(amount int) string {
	return encodeV4(v4Token{
		Mint: TestMint,
		Unit: "sat",
		Token: []v4Entry{{
			KeysetID: keysetID,
			Proofs:   []v4Proof{{Amount: amount, Secret: "test_secret_12345", C: c33}},
		}},
	})
}

func V4TokenWithMint(amount int, mint string) string {
	return encodeV4(v4Token{
		Mint: mint,
		Unit: "sat",
		Token: []v4Entry{{
			KeysetID: keysetID,
			Proofs:   []v4Proof{{Amount: amount, Secret: "test_secret_12345", C: c33}},
		}},
	})
}

func V4TokenZeroAmount() string {
	return encodeV4(v4Token{
		Mint: TestMint,
		Unit: "sat",
		Token: []v4Entry{{
			KeysetID: keysetID,
			Proofs:   []v4Proof{{Amount: 0, Secret: "test_secret_12345", C: c33}},
		}},
	})
}

func V4TokenDLEQ() string {
	secret := make([]byte, 178)
	for i := range secret {
		secret[i] = 'a' + byte(i%26)
	}
	return encodeV4(v4Token{
		Mint: TestMint,
		Unit: "sat",
		Token: []v4Entry{{
			KeysetID: keysetID,
			Proofs:   []v4Proof{{Amount: 8, Secret: string(secret), C: c33}},
		}},
	})
}

func V4TokenDLEQSplit() (first200, rest178 string) {
	full := V4TokenDLEQ()
	return full[:200], full[200:]
}

func V3Token(amount int) string {
	v3 := map[string]interface{}{
		"token": []map[string]interface{}{
			{
				"mint": TestMint,
				"proofs": []map[string]interface{}{
					{"amount": amount, "id": "00deadbeef", "secret": "test_secret_12345", "C": "02100b2c1b0f3a4d5e6f708192a3b4c5d6e7f809192a3b4c5d6e7f809192a3b4c5d"},
				},
			},
		},
		"unit": "sat",
	}
	b, _ := json.Marshal(v3)
	return "cashuA" + base64.RawURLEncoding.EncodeToString(b)
}

func LNURLwCode() string {
	return "lnurlw1234567890abcdefghijklmnopqrstuvwxyz"
}

func LNURLwUpper() string {
	return strings.ToUpper(LNURLwCode())
}

func InvalidGarbage() string {
	return "this-is-not-a-token"
}

func InvalidPrefix() string {
	return "cashuC" + strings.Repeat("A", 20)
}

func ShellInjection() string {
	return "cashuB" + base64.RawURLEncoding.EncodeToString([]byte(";rm -rf /"))
}

func NoDLEQToken() string {
	return V4Token(8)
}

func ValidLNURLw() string {
	return "lnurlwdp68gup6jhjumue2nn29"
}

func NonTestMintURL() string {
	return "https://mainnet.cashu.exchange"
}

func V4TokenNonTestMint(amount int) string {
	return V4TokenWithMint(amount, NonTestMintURL())
}
