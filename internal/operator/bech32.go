package operator

import (
	"fmt"
	"strings"
)

var bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

var bech32CharsetRev = func() [128]int8 {
	var t [128]int8
	for i := range t {
		t[i] = -1
	}
	for i, c := range bech32Charset {
		t[c] = int8(i)
	}
	return t
}()

func bech32Polymod(values []int) int {
	gen := []int{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := 1
	for _, v := range values {
		b := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ v
		for i := 0; i < 5; i++ {
			if (b>>uint(i))&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}

func bech32HrpExpand(hrp string) []int {
	ret := make([]int, 0, len(hrp)*2+1)
	for _, c := range hrp {
		ret = append(ret, int(c)>>5)
	}
	ret = append(ret, 0)
	for _, c := range hrp {
		ret = append(ret, int(c)&31)
	}
	return ret
}

func bech32VerifyChecksum(hrp string, data []int) bool {
	return bech32Polymod(append(bech32HrpExpand(hrp), data...)) == 1
}

// decodeBech32 decodes a bech32 string into (hrp, 5-bit data values excluding checksum).
func decodeBech32(s string) (hrp string, data []int, err error) {
	if strings.ToLower(s) != s && strings.ToUpper(s) != s {
		return "", nil, fmt.Errorf("bech32: mixed case")
	}
	s = strings.ToLower(s)
	pos := strings.LastIndex(s, "1")
	if pos < 1 || pos+7 > len(s) {
		return "", nil, fmt.Errorf("bech32: invalid separator position")
	}
	hrp = s[:pos]
	for _, c := range hrp {
		if c < 33 || c > 126 {
			return "", nil, fmt.Errorf("bech32: invalid HRP character")
		}
	}
	dataPart := s[pos+1:]
	data = make([]int, 0, len(dataPart))
	for _, c := range dataPart {
		if int(c) >= len(bech32CharsetRev) || bech32CharsetRev[c] == -1 {
			return "", nil, fmt.Errorf("bech32: invalid character %q in data part", c)
		}
		data = append(data, int(bech32CharsetRev[c]))
	}
	if !bech32VerifyChecksum(hrp, data) {
		return "", nil, fmt.Errorf("bech32: invalid checksum")
	}
	return hrp, data[:len(data)-6], nil
}

// convertBits converts data between bit group sizes.
func convertBits(data []int, frombits, tobits int, pad bool) ([]int, error) {
	acc := 0
	bits := 0
	var ret []int
	maxv := (1 << tobits) - 1
	for _, value := range data {
		if value < 0 || (value>>uint(frombits)) != 0 {
			return nil, fmt.Errorf("convertBits: invalid value %d", value)
		}
		acc = (acc << uint(frombits)) | value
		bits += frombits
		for bits >= tobits {
			bits -= tobits
			ret = append(ret, (acc>>uint(bits))&maxv)
		}
	}
	if pad {
		if bits > 0 {
			ret = append(ret, (acc<<uint(tobits-bits))&maxv)
		}
	} else if bits >= frombits || ((acc<<uint(tobits-bits))&maxv) != 0 {
		return nil, fmt.Errorf("convertBits: invalid padding")
	}
	return ret, nil
}

// decodeNsec decodes a Nostr nsec string to its 32-byte private key.
func decodeNsec(nsec string) ([]byte, error) {
	hrp, data, err := decodeBech32(nsec)
	if err != nil {
		return nil, fmt.Errorf("decode nsec: %w", err)
	}
	if hrp != "nsec" {
		return nil, fmt.Errorf("decode nsec: expected prefix 'nsec', got %q", hrp)
	}
	conv, err := convertBits(data, 5, 8, false)
	if err != nil {
		return nil, fmt.Errorf("decode nsec: %w", err)
	}
	if len(conv) != 32 {
		return nil, fmt.Errorf("decode nsec: expected 32 bytes, got %d", len(conv))
	}
	result := make([]byte, 32)
	for i, v := range conv {
		result[i] = byte(v)
	}
	return result, nil
}
