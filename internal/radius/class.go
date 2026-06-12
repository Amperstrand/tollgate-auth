package radius

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// SessionClass carries session metadata in the RADIUS Class attribute.
// Sent in Access-Accept, echoed back in accounting packets (RFC 2865 §5.5).
// HMAC-signed for tamper detection.
type SessionClass struct {
	OperatorID string `json:"op"` // operator identifier
	MAC        string `json:"mac"` // session identifier (Calling-Station-Id)
	TokenHash  string `json:"th"`  // first 16 chars of token hash
	Timestamp  int64  `json:"ts"`  // unix seconds
	Nonce      string `json:"n"`   // 8 random hex chars
}

const (
	// ClassPrefix identifies our Class attributes ("tollgate v1").
	ClassPrefix = "tg1:"

	// MaxClassLength is the maximum RADIUS Class attribute size (253 bytes).
	MaxClassLength = 253
)

// NewSessionClass creates a SessionClass with current timestamp and random nonce.
func NewSessionClass(operatorID, mac, tokenHash string) *SessionClass {
	return &SessionClass{
		OperatorID: operatorID,
		MAC:        mac,
		TokenHash:  tokenHash,
		Timestamp:  time.Now().Unix(),
		Nonce:      randomHex(4),
	}
}

// Encode serializes the SessionClass to a compact string.
// Format: "tg1:<base64url(json(SessionClass))>"
func (c *SessionClass) Encode() (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("radius class: json marshal failed: %w", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(data)
	result := ClassPrefix + encoded

	if len(result) > MaxClassLength {
		return "", fmt.Errorf("radius class: encoded length %d exceeds maximum %d", len(result), MaxClassLength)
	}

	return result, nil
}

// DecodeClass parses a Class attribute string back to SessionClass.
func DecodeClass(s string) (*SessionClass, error) {
	if len(s) < len(ClassPrefix) || s[:len(ClassPrefix)] != ClassPrefix {
		return nil, fmt.Errorf("radius class: invalid prefix %q", s)
	}

	encoded := s[len(ClassPrefix):]

	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("radius class: base64 decode failed: %w", err)
	}

	var sc SessionClass
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, fmt.Errorf("radius class: json unmarshal failed: %w", err)
	}

	return &sc, nil
}

// HMACSign encodes the SessionClass and appends an HMAC-SHA256 signature.
// Format: "tg1:<base64url(json)>.<hex(hmac)[:16]>"
// The HMAC covers the prefix before the dot (everything before the HMAC).
func (c *SessionClass) HMACSign(key []byte) (string, error) {
	encoded, err := c.Encode()
	if err != nil {
		return "", err
	}

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(encoded))
	sig := hex.EncodeToString(mac.Sum(nil))[:16]

	result := encoded + "." + sig

	if len(result) > MaxClassLength {
		return "", fmt.Errorf("radius class: signed length %d exceeds maximum %d", len(result), MaxClassLength)
	}

	return result, nil
}

// VerifyClass parses and verifies a HMAC-signed Class string.
// Returns the SessionClass if valid, error if tampered or malformed.
func VerifyClass(s string, key []byte) (*SessionClass, error) {
	// Split on last dot to separate payload from HMAC.
	lastDot := -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			lastDot = i
			break
		}
	}
	if lastDot < 0 {
		return nil, fmt.Errorf("radius class: no HMAC separator in signed class")
	}

	prefix := s[:lastDot]
	providedSig := s[lastDot+1:]

	// Recompute HMAC over the prefix.
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(prefix))
	expectedSig := hex.EncodeToString(mac.Sum(nil))[:16]

	// Constant-time comparison.
	if !hmac.Equal([]byte(providedSig), []byte(expectedSig)) {
		return nil, fmt.Errorf("radius class: HMAC verification failed")
	}

	return DecodeClass(prefix)
}

// randomHex generates n random bytes as a hex string (2n hex chars).
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
