package radius

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestNewSessionClass(t *testing.T) {
	sc := NewSessionClass("op-abc123", "aa:bb:cc:dd:ee:ff", "a1b2c3d4e5f6a7b8")

	if sc.OperatorID != "op-abc123" {
		t.Errorf("OperatorID = %q, want %q", sc.OperatorID, "op-abc123")
	}
	if sc.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("MAC = %q, want %q", sc.MAC, "aa:bb:cc:dd:ee:ff")
	}
	if sc.TokenHash != "a1b2c3d4e5f6a7b8" {
		t.Errorf("TokenHash = %q, want %q", sc.TokenHash, "a1b2c3d4e5f6a7b8")
	}
	if sc.Timestamp <= 0 {
		t.Errorf("Timestamp = %d, want positive unix time", sc.Timestamp)
	}
	if len(sc.Nonce) != 32 {
		t.Errorf("Nonce = %q (%d chars), want 32 hex chars", sc.Nonce, len(sc.Nonce))
	}
	// Verify nonce is valid hex.
	_, err := hex.DecodeString(sc.Nonce)
	if err != nil {
		t.Errorf("Nonce %q is not valid hex: %v", sc.Nonce, err)
	}
}

func TestSessionClass_EncodeDecode(t *testing.T) {
	original := &SessionClass{
		OperatorID: "op-test",
		MAC:        "11:22:33:44:55:66",
		TokenHash:  "deadbeef01020304",
		Timestamp:  1700000000,
		Nonce:      "aabbccdd",
	}

	encoded, err := original.Encode()
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	if !strings.HasPrefix(encoded, ClassPrefix) {
		t.Errorf("encoded %q doesn't start with prefix %q", encoded, ClassPrefix)
	}

	decoded, err := DecodeClass(encoded)
	if err != nil {
		t.Fatalf("DecodeClass failed: %v", err)
	}

	if decoded.OperatorID != original.OperatorID {
		t.Errorf("OperatorID = %q, want %q", decoded.OperatorID, original.OperatorID)
	}
	if decoded.MAC != original.MAC {
		t.Errorf("MAC = %q, want %q", decoded.MAC, original.MAC)
	}
	if decoded.TokenHash != original.TokenHash {
		t.Errorf("TokenHash = %q, want %q", decoded.TokenHash, original.TokenHash)
	}
	if decoded.Timestamp != original.Timestamp {
		t.Errorf("Timestamp = %d, want %d", decoded.Timestamp, original.Timestamp)
	}
	if decoded.Nonce != original.Nonce {
		t.Errorf("Nonce = %q, want %q", decoded.Nonce, original.Nonce)
	}
}

func TestSessionClass_EncodeMaxLength(t *testing.T) {
	sc := &SessionClass{
		OperatorID: "op-test",
		MAC:        "aa:bb:cc:dd:ee:ff",
		TokenHash:  "0123456789abcdef",
		Timestamp:  1700000000,
		Nonce:      "aabbccdd",
	}

	encoded, err := sc.Encode()
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	if len(encoded) > MaxClassLength {
		t.Errorf("encoded length %d exceeds max %d: %q", len(encoded), MaxClassLength, encoded)
	}
}

func TestDecodeClass_InvalidPrefix(t *testing.T) {
	_, err := DecodeClass("other:somedata")
	if err == nil {
		t.Fatal("expected error for invalid prefix, got nil")
	}
	if !strings.Contains(err.Error(), "invalid prefix") {
		t.Errorf("error = %q, want 'invalid prefix'", err.Error())
	}
}

func TestDecodeClass_InvalidBase64(t *testing.T) {
	_, err := DecodeClass("tg1:!!!invalid!!!")
	if err == nil {
		t.Fatal("expected error for invalid base64, got nil")
	}
	if !strings.Contains(err.Error(), "base64 decode") {
		t.Errorf("error = %q, want 'base64 decode'", err.Error())
	}
}

func TestHMACSignVerify(t *testing.T) {
	sc := NewSessionClass("op-test", "aa:bb:cc:dd:ee:ff", "0123456789abcdef")
	key := []byte("test-hmac-key-1234567890123456")

	signed, err := sc.HMACSign(key)
	if err != nil {
		t.Fatalf("HMACSign failed: %v", err)
	}

	if !strings.HasPrefix(signed, ClassPrefix) {
		t.Errorf("signed %q doesn't start with prefix %q", signed, ClassPrefix)
	}

	// Verify should succeed with the same key.
	decoded, err := VerifyClass(signed, key)
	if err != nil {
		t.Fatalf("VerifyClass failed: %v", err)
	}

	if decoded.OperatorID != sc.OperatorID {
		t.Errorf("OperatorID = %q, want %q", decoded.OperatorID, sc.OperatorID)
	}
	if decoded.MAC != sc.MAC {
		t.Errorf("MAC = %q, want %q", decoded.MAC, sc.MAC)
	}
	if decoded.TokenHash != sc.TokenHash {
		t.Errorf("TokenHash = %q, want %q", decoded.TokenHash, sc.TokenHash)
	}
}

func TestVerifyClass_TamperedPayload(t *testing.T) {
	sc := NewSessionClass("op-test", "aa:bb:cc:dd:ee:ff", "0123456789abcdef")
	key := []byte("test-hmac-key-1234567890123456")

	signed, err := sc.HMACSign(key)
	if err != nil {
		t.Fatalf("HMACSign failed: %v", err)
	}

	// Tamper with one character in the base64 payload (before the dot).
	dotIdx := strings.LastIndex(signed, ".")
	if dotIdx < 0 {
		t.Fatal("no dot separator in signed class")
	}

	tampered := signed[:dotIdx-1] + "X" + signed[dotIdx:]

	_, err = VerifyClass(tampered, key)
	if err == nil {
		t.Fatal("expected error for tampered payload, got nil")
	}
}

func TestVerifyClass_TamperedHMAC(t *testing.T) {
	sc := NewSessionClass("op-test", "aa:bb:cc:dd:ee:ff", "0123456789abcdef")
	key := []byte("test-hmac-key-1234567890123456")

	signed, err := sc.HMACSign(key)
	if err != nil {
		t.Fatalf("HMACSign failed: %v", err)
	}

	// Tamper with one character in the HMAC hex (after the dot).
	dotIdx := strings.LastIndex(signed, ".")
	if dotIdx < 0 {
		t.Fatal("no dot separator in signed class")
	}

	hmacPart := signed[dotIdx+1:]
	// Flip one hex char.
	tamperedHMAC := "ff" + hmacPart[2:]
	if tamperedHMAC == hmacPart {
		tamperedHMAC = "00" + hmacPart[2:]
	}
	tampered := signed[:dotIdx+1] + tamperedHMAC

	_, err = VerifyClass(tampered, key)
	if err == nil {
		t.Fatal("expected error for tampered HMAC, got nil")
	}
}

func TestVerifyClass_WrongKey(t *testing.T) {
	sc := NewSessionClass("op-test", "aa:bb:cc:dd:ee:ff", "0123456789abcdef")
	signKey := []byte("signing-key-12345678901234567")
	verifyKey := []byte("wrong-key-00000000000000000000")

	signed, err := sc.HMACSign(signKey)
	if err != nil {
		t.Fatalf("HMACSign failed: %v", err)
	}

	_, err = VerifyClass(signed, verifyKey)
	if err == nil {
		t.Fatal("expected error for wrong key, got nil")
	}
}

func TestVerifyClass_ValidRoundTrip(t *testing.T) {
	// Full encode → sign → verify → decode round trip.
	key := []byte("round-trip-test-key-123456789012")
	original := NewSessionClass("op-xyz789", "dd:ee:ff:00:11:22", "abcdef0123456789")

	signed, err := original.HMACSign(key)
	if err != nil {
		t.Fatalf("HMACSign failed: %v", err)
	}

	if len(signed) > MaxClassLength {
		t.Errorf("signed length %d exceeds max %d", len(signed), MaxClassLength)
	}

	decoded, err := VerifyClass(signed, key)
	if err != nil {
		t.Fatalf("VerifyClass failed: %v", err)
	}

	if decoded.OperatorID != original.OperatorID {
		t.Errorf("OperatorID = %q, want %q", decoded.OperatorID, original.OperatorID)
	}
	if decoded.MAC != original.MAC {
		t.Errorf("MAC = %q, want %q", decoded.MAC, original.MAC)
	}
	if decoded.TokenHash != original.TokenHash {
		t.Errorf("TokenHash = %q, want %q", decoded.TokenHash, original.TokenHash)
	}
	if decoded.Timestamp != original.Timestamp {
		t.Errorf("Timestamp = %d, want %d", decoded.Timestamp, original.Timestamp)
	}
	if decoded.Nonce != original.Nonce {
		t.Errorf("Nonce = %q, want %q", decoded.Nonce, original.Nonce)
	}
}

func TestRandomHex(t *testing.T) {
	h := randomHex(4) // 8 hex chars
	if len(h) != 8 {
		t.Errorf("randomHex(4) = %q (%d chars), want 8 chars", h, len(h))
	}

	// Must be valid hex.
	_, err := hex.DecodeString(h)
	if err != nil {
		t.Errorf("randomHex(4) = %q is not valid hex: %v", h, err)
	}

	// Different calls should produce different values (probabilistic).
	h2 := randomHex(4)
	if h == h2 {
		t.Logf("warning: two randomHex calls produced same value %q (unlikely but possible)", h)
	}
}

func TestNewSessionClass_UniqueNonces(t *testing.T) {
	sc1 := NewSessionClass("op", "mac", "hash")
	sc2 := NewSessionClass("op", "mac", "hash")

	if sc1.Nonce == sc2.Nonce {
		t.Errorf("two NewSessionClass calls produced same nonce %q", sc1.Nonce)
	}
}
