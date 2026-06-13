// Package redact provides safe logging helpers that prevent sensitive
// payment credentials (Cashu tokens, LNURLw codes) from appearing
// verbatim in log output.
package redact

import "strings"

// Token redacts a sensitive string for log output.
// For strings longer than 12 characters it returns the first 8 characters
// + "..." + last 4 characters. Shorter strings are replaced with "[REDACTED]".
func Token(s string) string {
	if len(s) <= 12 {
		return "[REDACTED]"
	}
	return s[:8] + "..." + s[len(s)-4:]
}

// LogSafe sanitizes a string for log output by replacing newlines
// and carriage returns with their escaped representations.
func LogSafe(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	return s
}

// Truncate shortens a string to at most n characters, appending "..."
// if truncation occurred.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
