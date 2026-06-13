package radiusauth

import (
	"strings"
	"testing"

	"tollgate-auth/internal/testtoken"
)

// --- ExtractPayment exhaustive tests ---

func TestExtractPayment(t *testing.T) {
	fullToken378 := testtoken.V4TokenDLEQ()
	noDLEQToken := testtoken.NoDLEQToken()

	if !IsValidCashuToken(fullToken378) {
		t.Fatal("378-byte token should be a valid cashu token")
	}
	if !IsValidCashuToken(noDLEQToken) {
		t.Fatal("no-DLEQ token should be a valid cashu token")
	}
	if len(fullToken378) != 378 {
		t.Fatalf("378-byte token is %d bytes", len(fullToken378))
	}

	splitPW, splitUN := testtoken.V4TokenDLEQSplit()

	validLNURLw := testtoken.ValidLNURLw()

	// A V3 token for testing cashuA prefix
	v3Token := "cashuAeyJ0b2tlbiI6W3sibWludCI6Imh0dHBzOi8vdGVzdG51dC5jYXNodS5zcGFjZSIsInByb29mcyI6W3siYW1vdW50Ijo4LCJpZCI6ImsiLCJzZWNyZXQiOiJzIiwiQyI6ImMifV19XSwidW5pdCI6InNhdCJ9"

	tests := []struct {
		name         string
		username     string
		password     string
		clearTextPw  string
		wantFound    bool
		wantSource   string // expected Source field
		wantType     PaymentType
		wantValueLen int    // expected len of Value (0 means check exact match with input)
		wantValue    string // if non-empty, exact expected Value
	}{
		// --- Cashu token in username ---
		{
			name:        "cashuA token in username",
			username:    v3Token,
			password:    "",
			clearTextPw: "",
			wantFound:   true,
			wantSource:  "username",
			wantType:    PaymentCashu,
			wantValue:   v3Token,
		},
		{
			name:        "cashuB token in username",
			username:    noDLEQToken,
			password:    "",
			clearTextPw: "",
			wantFound:   true,
			wantSource:  "username",
			wantType:    PaymentCashu,
			wantValue:   noDLEQToken,
		},

		// --- Cashu token in password ---
		{
			name:        "cashuB token in password",
			username:    "user1",
			password:    noDLEQToken,
			clearTextPw: "",
			wantFound:   true,
			wantSource:  "password",
			wantType:    PaymentCashu,
			wantValue:   noDLEQToken,
		},

		// --- Cashu token in cleartext-password ---
		{
			name:        "cashuB token in cleartext-password",
			username:    "user1",
			password:    "",
			clearTextPw: noDLEQToken,
			wantFound:   true,
			wantSource:  "cleartext-password",
			wantType:    PaymentCashu,
			wantValue:   noDLEQToken,
		},

		// --- LNURLw in username ---
		{
			name:        "LNURLw in username",
			username:    validLNURLw,
			password:    "",
			clearTextPw: "",
			wantFound:   true,
			wantSource:  "username",
			wantType:    PaymentLNURLW,
			wantValue:   validLNURLw,
		},

		// --- LNURLw in password ---
		{
			name:        "LNURLw in password",
			username:    "user1",
			password:    validLNURLw,
			clearTextPw: "",
			wantFound:   true,
			wantSource:  "password",
			wantType:    PaymentLNURLW,
			wantValue:   validLNURLw,
		},

		// --- LNURLw in cleartext-password ---
		{
			name:        "LNURLw in cleartext-password",
			username:    "user1",
			password:    "",
			clearTextPw: validLNURLw,
			wantFound:   true,
			wantSource:  "cleartext-password",
			wantType:    PaymentLNURLW,
			wantValue:   validLNURLw,
		},

		// --- Uppercase LNURLW in username ---
		{
			name:        "uppercase LNURLW in username",
			username:    strings.ToUpper(validLNURLw),
			password:    "",
			clearTextPw: "",
			wantFound:   true,
			wantSource:  "username",
			wantType:    PaymentLNURLW,
			wantValue:   strings.ToUpper(validLNURLw),
		},

		// --- Split token: password is first 200 bytes, username is base64url tail ---
		{
			name:        "split token via password + username",
			username:    splitUN,
			password:    splitPW,
			clearTextPw: "",
			wantFound:   true,
			wantSource:  "split-password+username",
			wantType:    PaymentCashu,
			wantValue:   fullToken378,
		},

		// --- Split token via cleartext-password + username ---
		{
			name:        "split token via cleartext-password + username",
			username:    splitUN,
			password:    "",
			clearTextPw: splitPW,
			wantFound:   true,
			wantSource:  "split-cleartext+username",
			wantType:    PaymentCashu,
			wantValue:   fullToken378,
		},

		// --- Split token malformed: password is 200 bytes but username is empty ---
		{
			name:        "split malformed: empty username",
			username:    "",
			password:    splitPW,
			clearTextPw: "",
			wantFound:   false,
		},

		// --- Split token malformed: password is 200 bytes but username has cashu prefix ---
		// cashuBabc123xyz is a valid cashu token, so it matches as username first
		{
			name:        "split malformed: username has cashu prefix",
			username:    "cashuBabc123xyz",
			password:    splitPW,
			clearTextPw: "",
			wantFound:   true,
			wantSource:  "username",
			wantType:    PaymentCashu,
			wantValue:   "cashuBabc123xyz",
		},

		// --- Split token malformed: password is 200 bytes but username has lnurlw prefix ---
		{
			name:        "split malformed: username has lnurlw prefix",
			username:    validLNURLw,
			password:    splitPW,
			clearTextPw: "",
			wantFound:   true,
			wantSource:  "username",
			wantType:    PaymentLNURLW,
			wantValue:   validLNURLw, // LNURLw in username is checked BEFORE split logic
		},

		// --- No-DLEQ token (~230 bytes) in password — should NOT trigger split ---
		{
			name:        "no-DLEQ 230-byte token in password (not split)",
			username:    "user1",
			password:    noDLEQToken,
			clearTextPw: "",
			wantFound:   true,
			wantSource:  "password",
			wantType:    PaymentCashu,
			wantValue:   noDLEQToken,
		},

		// --- Full token in password > tokenSplitLen — should match as full token ---
		{
			name:        "full 378-byte token in password",
			username:    "user1",
			password:    fullToken378,
			clearTextPw: "",
			wantFound:   true,
			wantSource:  "password",
			wantType:    PaymentCashu,
			wantValue:   fullToken378,
		},

		// --- No token found — empty username and password ---
		{
			name:        "no token: empty fields",
			username:    "",
			password:    "",
			clearTextPw: "",
			wantFound:   false,
		},

		// --- Invalid input: too long (> MaxInputLen) — split token rejected ---
		{
			name:        "split rejected: reassembled token exceeds MaxInputLen",
			username:    strings.Repeat("A", MaxInputLen),
			password:    splitPW,
			clearTextPw: "",
			wantFound:   false, // sanitizeInput rejects the concatenated token
		},

		// --- Invalid input: shell metacharacters in username for split ---
		{
			name:        "split rejected: shell metachar in username",
			username:    "abc;rm -rf",
			password:    splitPW,
			clearTextPw: "",
			wantFound:   false, // sanitizeInput rejects the concatenated token (contains ;)
		},

		// --- Empty fields ---
		{
			name:        "no token: username is empty, password is plain",
			username:    "",
			password:    "justapassword",
			clearTextPw: "",
			wantFound:   false,
		},
		{
			name:        "no token: all fields are regular strings",
			username:    "alice",
			password:    "password123",
			clearTextPw: "password456",
			wantFound:   false,
		},

		// --- Token in username takes priority over token in password ---
		{
			name:        "username priority: both username and password have tokens",
			username:    noDLEQToken,
			password:    fullToken378,
			clearTextPw: "",
			wantFound:   true,
			wantSource:  "username",
			wantType:    PaymentCashu,
			wantValue:   noDLEQToken,
		},

		// --- Cashu token in username takes priority over LNURLw in password ---
		{
			name:        "username cashu beats password LNURLw",
			username:    noDLEQToken,
			password:    validLNURLw,
			clearTextPw: "",
			wantFound:   true,
			wantSource:  "username",
			wantType:    PaymentCashu,
			wantValue:   noDLEQToken,
		},

		// --- LNURLw in username takes priority over LNURLw in password ---
		{
			name:        "LNURLw username beats LNURLw password",
			username:    validLNURLw,
			password:    "lnurlwdifferentcode12345678",
			clearTextPw: "",
			wantFound:   true,
			wantSource:  "username",
			wantType:    PaymentLNURLW,
			wantValue:   validLNURLw,
		},

		// --- Invalid cashu token (shell metachar) in username is rejected ---
		{
			name:        "invalid cashu token in username (contains semicolon)",
			username:    "cashuBabc;rm",
			password:    "",
			clearTextPw: "",
			wantFound:   false,
		},

		// --- Short cashu prefix (too short to be valid) ---
		{
			name:        "cashuB prefix too short",
			username:    "cashuBab",
			password:    "",
			clearTextPw: "",
			wantFound:   false,
		},

		// --- Split: password is 200 bytes but NOT starting with valid cashuB ---
		// This can't happen with a real token, but test the guard
		{
			name:        "split rejected: password is 200 bytes but not valid cashu",
			username:    strings.Repeat("A", 178),
			password:    strings.Repeat("B", 200),
			clearTextPw: "",
			wantFound:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cred, found := ExtractPayment(tc.username, tc.password, tc.clearTextPw)
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v", found, tc.wantFound)
			}
			if !tc.wantFound {
				return
			}
			if cred.Source != tc.wantSource {
				t.Errorf("source = %q, want %q", cred.Source, tc.wantSource)
			}
			if cred.Type != tc.wantType {
				t.Errorf("type = %q, want %q", cred.Type, tc.wantType)
			}
			if tc.wantValue != "" && cred.Value != tc.wantValue {
				// Truncate for safe error output
				gotPrefix := cred.Value
				wantPrefix := tc.wantValue
				if len(gotPrefix) > 40 {
					gotPrefix = gotPrefix[:20] + "..." + gotPrefix[len(gotPrefix)-20:]
				}
				if len(wantPrefix) > 40 {
					wantPrefix = wantPrefix[:20] + "..." + wantPrefix[len(wantPrefix)-20:]
				}
				t.Errorf("value mismatch:\n  got  (len=%d): %s\n  want (len=%d): %s",
					len(cred.Value), gotPrefix, len(tc.wantValue), wantPrefix)
			}
			if tc.wantValueLen > 0 && len(cred.Value) != tc.wantValueLen {
				t.Errorf("value length = %d, want %d", len(cred.Value), tc.wantValueLen)
			}
		})
	}
}

// --- Unit tests for individual validators ---

func TestIsCashuToken(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"cashuA prefix", "cashuAabc123", true},
		{"cashuB prefix", "cashuBabc123", true},
		{"no prefix", "something", false},
		{"empty", "", false},
		{"partial prefix", "cashu", false},
		{"cashuC prefix", "cashuCabc", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCashuToken(tc.input); got != tc.want {
				t.Errorf("IsCashuToken(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsValidCashuToken(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid cashuA", "cashuAabc123XYZ", true},
		{"valid cashuB", "cashuBabc123_XYZ-def", true},
		{"too short", "cashuAabc", false},
		{"no prefix", "somethingelse", false},
		{"shell metachar semicolon", "cashuAabc;rm", false},
		{"shell metachar dollar", "cashuAabc$var", false},
		{"space in token", "cashuAabc def", false},
		{"newline in token", "cashuAabc\ndef", false},
		{"tab in token", "cashuAabc\tdef", false},
		{"empty", "", false},
		{"just prefix", "cashuA", false},
		{"valid with hyphen underscore", "cashuBabc_def-XYZ123", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidCashuToken(tc.input); got != tc.want {
				t.Errorf("IsValidCashuToken(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsLNURLw(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid lowercase", "lnurlwabc1234567", true},
		{"valid uppercase", "LNURLWABC1234567", true},
		{"too short", "lnurlwabc", false},
		{"no prefix", "somethingelse", false},
		{"empty", "", false},
		{"mixed case", "LNURLwABC123456", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsLNURLw(tc.input); got != tc.want {
				t.Errorf("IsLNURLw(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsValidLNURLw(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid lowercase", "lnurlwabc1234567", true},
		{"valid uppercase", "LNURLWABC1234567", true},
		{"too short", "lnurlwabc", false},
		{"contains hyphen", "lnurlwabc-12345", false},
		{"contains underscore", "lnurlwabc_12345", false},
		{"contains space", "lnurlwabc 12345", false},
		{"no prefix", "somethingelse", false},
		{"empty", "", false},
		{"mixed case valid", "lnurlwABCdef12345", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidLNURLw(tc.input); got != tc.want {
				t.Errorf("IsValidLNURLw(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsBase64url(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid", "abcDEF123_-", true},
		{"empty", "", false},
		{"spaces", "abc def", false},
		{"plus sign", "abc+def", false},
		{"equals", "abc=def", false},
		{"alphanumeric only", "abcXYZ0123456789", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsBase64url(tc.input); got != tc.want {
				t.Errorf("IsBase64url(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestSanitizeMAC(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantMac string
		wantOk  bool
	}{
		{"valid lowercase colon-separated", "aa:bb:cc:dd:ee:ff", "aabbccddeeff", true},
		{"valid uppercase colon-separated", "AA:BB:CC:DD:EE:FF", "aabbccddeeff", true},
		{"valid dash-separated", "aa-bb-cc-dd-ee-ff", "aabbccddeeff", true},
		{"valid mixed separators", "aa:bb-cc:dd-ee:ff", "aabbccddeeff", true},
		{"empty", "", "", true},
		{"path traversal", "../etc/passwd", "", false},
		{"spaces", "aa bb cc dd ee ff", "", false},
		{"special chars", "aa;bb", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mac, ok := SanitizeMAC(tc.input)
			if ok != tc.wantOk {
				t.Errorf("ok = %v, want %v", ok, tc.wantOk)
			}
			if ok && mac != tc.wantMac {
				t.Errorf("mac = %q, want %q", mac, tc.wantMac)
			}
		})
	}
}
