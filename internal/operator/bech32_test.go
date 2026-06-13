package operator

import (
	"fmt"
	"strings"
	"testing"
)

// bech32CreateChecksum computes the checksum for a given hrp and data.
func bech32CreateChecksum(hrp string, data []int) []int {
	values := append(bech32HrpExpand(hrp), data...)
	values = append(values, 0, 0, 0, 0, 0, 0)
	mod := bech32Polymod(values) ^ 1
	ret := make([]int, 6)
	for i := 0; i < 6; i++ {
		ret[i] = (mod >> uint(5*(5-i))) & 31
	}
	return ret
}

// bech32Encode encodes hrp + data as a bech32 string.
func bech32Encode(hrp string, data []int) string {
	values := append(data, bech32CreateChecksum(hrp, data)...)
	var sb strings.Builder
	sb.WriteString(hrp)
	sb.WriteByte('1')
	for _, v := range values {
		sb.WriteByte(bech32Charset[v])
	}
	return sb.String()
}

// encodeNsecForTest encodes 32 raw bytes as an nsec string (for test use).
func encodeNsecForTest(key []byte) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("expected 32 bytes, got %d", len(key))
	}
	data := make([]int, len(key))
	for i, b := range key {
		data[i] = int(b)
	}
	conv, err := convertBits(data, 8, 5, true)
	if err != nil {
		return "", err
	}
	return bech32Encode("nsec", conv), nil
}

func TestDecodeNsec_RoundTrip(t *testing.T) {
	known := [32]byte{
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
	}

	encoded, err := encodeNsecForTest(known[:])
	if err != nil {
		t.Fatalf("encodeNsecForTest() error: %v", err)
	}
	if !strings.HasPrefix(encoded, "nsec1") {
		t.Errorf("encoded nsec should start with 'nsec1', got %q", encoded)
	}

	decoded, err := decodeNsec(encoded)
	if err != nil {
		t.Fatalf("decodeNsec() error: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("len(decoded) = %d, want 32", len(decoded))
	}
	if string(decoded) != string(known[:]) {
		t.Errorf("round-trip mismatch: got %x, want %x", decoded, known)
	}
}

func TestDecodeNsec_InvalidPrefix(t *testing.T) {
	known := [32]byte{
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
	}
	data := make([]int, len(known))
	for i, b := range known {
		data[i] = int(b)
	}
	conv, err := convertBits(data, 8, 5, true)
	if err != nil {
		t.Fatalf("convertBits() error: %v", err)
	}
	// Same payload but valid checksum under "npub" HRP
	npubValue := bech32Encode("npub", conv)

	_, err = decodeNsec(npubValue)
	if err == nil {
		t.Fatal("decodeNsec() with npub prefix expected error, got nil")
	}
	if !strings.Contains(err.Error(), "expected prefix 'nsec'") {
		t.Errorf("error should mention expected prefix 'nsec', got: %v", err)
	}
}

func TestDecodeNsec_InvalidChecksum(t *testing.T) {
	known := [32]byte{
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
	}
	encoded, err := encodeNsecForTest(known[:])
	if err != nil {
		t.Fatalf("encodeNsecForTest() error: %v", err)
	}
	// Corrupt one character in the data part
	runes := []rune(encoded)
	runes[len(runes)-1] = 'q' // flip last char to a different value
	corrupted := string(runes)

	_, err = decodeNsec(corrupted)
	if err == nil {
		t.Fatal("decodeNsec() with corrupted checksum expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid checksum") {
		t.Errorf("error should mention invalid checksum, got: %v", err)
	}
}

func TestDecodeNsec_WrongLength(t *testing.T) {
	short := make([]byte, 31)
	for i := range short {
		short[i] = byte(i)
	}
	data := make([]int, len(short))
	for i, b := range short {
		data[i] = int(b)
	}
	conv, err := convertBits(data, 8, 5, true)
	if err != nil {
		t.Fatalf("convertBits() error: %v", err)
	}
	encoded := bech32Encode("nsec", conv)

	_, err = decodeNsec(encoded)
	if err == nil {
		t.Fatal("decodeNsec() with 31-byte payload expected error, got nil")
	}
	if !strings.Contains(err.Error(), "expected 32 bytes") {
		t.Errorf("error should mention expected 32 bytes, got: %v", err)
	}
}
