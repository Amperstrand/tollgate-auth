// Package radiusauth extracts payment credentials (Cashu tokens, LNURLw codes)
// from RADIUS username, password, and cleartext-password fields.
//
// This package performs only string matching and validation — no network calls,
// no token decoding, no mint verification. It is purely about detecting which
// field contains a payment credential and what type it is.
package radiusauth

import (
	"regexp"
	"strings"
)

// PaymentType identifies the kind of payment credential.
type PaymentType string

const (
	PaymentCashu  PaymentType = "cashu"
	PaymentLNURLW PaymentType = "lnurlw"
)

// PaymentCredential holds an extracted payment string and its type.
type PaymentCredential struct {
	Value  string
	Source string // "username", "password", "cleartext-password", "split-password+username", "split-cleartext+username"
	Type   PaymentType
}

// Constants for input validation and split-token detection.
const (
	// TokenSplitLen is the byte length at which Cashu DLEQ tokens are split
	// across RADIUS password and username fields (378-byte token → 200 + 178).
	// No-DLEQ tokens are ~230 bytes and fit in a single field — no split needed.
	TokenSplitLen = 200

	// MaxInputLen is the maximum allowed length for username/password fields
	// to prevent abuse.
	MaxInputLen = 8192
)

// macPattern validates Calling-Station-Id: hex digits, colons, dashes, dots, or empty.
var macPattern = regexp.MustCompile(`^[0-9a-fA-F:\-\.]*$`)

// base64urlPattern matches strings that contain only base64url characters.
// Used to detect the tail portion of a split Cashu token in the username field.
var base64urlPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// SanitizeMAC strips separators and validates MAC address format.
// Returns the clean hex-only MAC and whether it's valid.
// Rejects path traversal attempts (e.g. "../").
func SanitizeMAC(mac string) (string, bool) {
	if !macPattern.MatchString(mac) {
		return "", false
	}
	clean := strings.ToLower(mac)
	clean = strings.ReplaceAll(clean, ":", "")
	clean = strings.ReplaceAll(clean, "-", "")
	return clean, true
}

// IsCashuToken checks if a string starts with a Cashu token prefix (cashuA or cashuB).
// Used for quick prefix checks before full validation.
func IsCashuToken(s string) bool {
	return strings.HasPrefix(s, "cashuA") || strings.HasPrefix(s, "cashuB")
}

// IsValidCashuToken validates that a string looks like a real Cashu token.
// Cashu V3 tokens: cashuA + base64url characters
// Cashu V4 tokens: cashuB + base64url characters
// No shell metacharacters, no control chars, no whitespace.
func IsValidCashuToken(s string) bool {
	if len(s) < 10 {
		return false
	}
	if !strings.HasPrefix(s, "cashuA") && !strings.HasPrefix(s, "cashuB") {
		return false
	}
	// After prefix: only base64url chars (A-Z, a-z, 0-9, -, _)
	for _, c := range s[6:] {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '=') {
			return false
		}
	}
	return true
}

// IsLNURLw checks if a string starts with an LNURL-withdraw prefix.
// Used for quick prefix checks before full validation.
func IsLNURLw(s string) bool {
	if len(s) < 10 {
		return false
	}
	lower := strings.ToLower(s)
	return strings.HasPrefix(lower, "lnurlw")
}

// IsValidLNURLw validates that a string looks like a real LNURL-withdraw code.
// LNURLw is bech32 encoded: alphanumeric only (A-Za-z0-9).
func IsValidLNURLw(s string) bool {
	if len(s) < 10 {
		return false
	}
	lower := strings.ToLower(s)
	if !strings.HasPrefix(lower, "lnurlw") {
		return false
	}
	for _, c := range s {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// IsBase64url checks whether a string contains only base64url characters.
func IsBase64url(s string) bool {
	return len(s) > 0 && base64urlPattern.MatchString(s)
}

// sanitizeInput rejects strings containing shell metacharacters or control chars.
// Rejects: ' \ ; ` $ ( ) | & > < \n \r \0 and strings exceeding MaxInputLen.
func sanitizeInput(s string) bool {
	if len(s) > MaxInputLen {
		return false
	}
	return !strings.ContainsAny(s, "'\\;`$()|&><\n\r\x00")
}

// ExtractPayment finds a payment credential (Cashu token or LNURLw) in username or password.
//
// Detection order:
//  1. Full token in username (cashuA/B prefix in username field)
//  2. LNURLw in username
//  3. LNURLw in password
//  4. LNURLw in cleartext-password
//  5. Split token (password == TokenSplitLen starts with cashuB, username is base64url tail)
//  6. Split token via cleartext-password + username
//  7. Full token in password (cashuA/B prefix, must be > TokenSplitLen)
//  8. Full token in cleartext-password
//
// The length guard on step 5 prevents misinterpreting a valid 230-byte no-DLEQ token
// (which starts with "cashuB" just like a split password) as a split. Split passwords
// are always exactly TokenSplitLen bytes by construction in the mint script.
func ExtractPayment(username, password, clearTextPw string) (PaymentCredential, bool) {
	// Full token in username
	if IsValidCashuToken(username) {
		return PaymentCredential{Value: username, Source: "username", Type: PaymentCashu}, true
	}
	if IsValidLNURLw(username) {
		return PaymentCredential{Value: username, Source: "username", Type: PaymentLNURLW}, true
	}
	if IsValidLNURLw(password) {
		return PaymentCredential{Value: password, Source: "password", Type: PaymentLNURLW}, true
	}
	if IsValidLNURLw(clearTextPw) {
		return PaymentCredential{Value: clearTextPw, Source: "cleartext-password", Type: PaymentLNURLW}, true
	}
	// Split token: password is the first TokenSplitLen bytes of a 378-byte DLEQ token.
	// Only triggers when password starts with cashuB AND is exactly TokenSplitLen bytes
	// AND username is base64url-only.
	if IsValidCashuToken(password) && len(password) == TokenSplitLen &&
		IsBase64url(username) && !IsCashuToken(username) && !IsLNURLw(username) {
		fullToken := password + username
		if sanitizeInput(fullToken) {
			return PaymentCredential{Value: fullToken, Source: "split-password+username", Type: PaymentCashu}, true
		}
	}
	if IsValidCashuToken(clearTextPw) && len(clearTextPw) == TokenSplitLen &&
		IsBase64url(username) && !IsCashuToken(username) && !IsLNURLw(username) {
		fullToken := clearTextPw + username
		if sanitizeInput(fullToken) {
			return PaymentCredential{Value: fullToken, Source: "split-cleartext+username", Type: PaymentCashu}, true
		}
	}
	// Full token in password (no-DLEQ 230b or any complete token > TokenSplitLen).
	// Skip passwords that are exactly TokenSplitLen — those are split candidates
	// that failed validation above, not standalone tokens.
	if IsValidCashuToken(password) && len(password) != TokenSplitLen {
		return PaymentCredential{Value: password, Source: "password", Type: PaymentCashu}, true
	}
	if IsValidCashuToken(clearTextPw) && len(clearTextPw) != TokenSplitLen {
		return PaymentCredential{Value: clearTextPw, Source: "cleartext-password", Type: PaymentCashu}, true
	}
	return PaymentCredential{}, false
}
