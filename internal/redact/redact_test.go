package redact

import (
	"strings"
	"testing"
)

func TestToken(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", "", "[REDACTED]"},
		{"single char", "a", "[REDACTED]"},
		{"short string (5 chars)", "hello", "[REDACTED]"},
		{"exactly 12 chars", "123456789012", "[REDACTED]"},
		{"13 chars — first above threshold", "1234567890123", "12345678...0123"},
		{"typical cashu token prefix", "cashuBxYzAbCdEfGhIjKlMnOpQrStUvWxYz", "cashuBxY...WxYz"},
		{"lnurlw code", "lnurlw1234567890abcdef", "lnurlw12...cdef"},
		{"long token preserves first 8 and last 4", strings.Repeat("A", 100), "AAAAAAAA...AAAA"},
		{"15 chars", "ABCDEFGHIJKLMNO", "ABCDEFGH...LMNO"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Token(tc.input)
			if got != tc.want {
				t.Errorf("Token(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestLogSafe(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", "", ""},
		{"no special chars", "hello world", "hello world"},
		{"newline replaced", "hello\nworld", "hello\\nworld"},
		{"carriage return replaced", "hello\rworld", "hello\\rworld"},
		{"both newline and carriage return", "hello\r\nworld", "hello\\r\\nworld"},
		{"multiple newlines", "a\nb\nc", "a\\nb\\nc"},
		{"tabs preserved", "hello\tworld", "hello\tworld"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := LogSafe(tc.input)
			if got != tc.want {
				t.Errorf("LogSafe(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name  string
		input string
		n     int
		want  string
	}{
		{"empty string, limit 10", "", 10, ""},
		{"shorter than limit", "hello", 10, "hello"},
		{"exactly at limit", "hello", 5, "hello"},
		{"longer than limit", "hello world", 5, "hello..."},
		{"limit 0", "hello", 0, "..."},
		{"limit 1", "abc", 1, "a..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Truncate(tc.input, tc.n)
			if got != tc.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tc.input, tc.n, got, tc.want)
			}
		})
	}
}
