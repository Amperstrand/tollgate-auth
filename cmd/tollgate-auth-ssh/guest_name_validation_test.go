package main

import (
	"testing"
)

// TestValidateGuestName_accepts_current_construction verifies that the
// hash-derived guest names actually produced by guestUsername()
// ("g-" + 8 hex chars) all pass validation.
func TestValidateGuestName_accepts_current_construction(t *testing.T) {
	valid := []string{
		"g-c3aa7bfb",
		"g-01234567",
		"g-abcdef01",
		"g-deadbeef",
		"radius-c3aa7bfb",
		"radius-delegated-c3aa7bfb",
		"radius-lnurlw-c3aa7bfb",
	}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			if _, err := validateGuestName(name); err != nil {
				t.Errorf("expected %q to be accepted, got error: %v", name, err)
			}
		})
	}
}

// TestValidateGuestName_rejects_path_traversal verifies that the validator
// blocks any name that could escape SessionDir via path traversal — the
// exact defense-in-depth scenario documented in createJail.
func TestValidateGuestName_rejects_path_traversal(t *testing.T) {
	attacks := []string{
		"../etc",
		"../../etc/passwd",
		"..",
		"./etc",
		"g-../etc",
		"g-..",
		"/etc/passwd",
		"g-/etc",
	}
	for _, attack := range attacks {
		t.Run(attack, func(t *testing.T) {
			if _, err := validateGuestName(attack); err == nil {
				t.Errorf("expected %q to be REJECTED (path traversal), but it was accepted", attack)
			}
		})
	}
}

// TestValidateGuestName_rejects_shell_metachars verifies that the validator
// blocks shell metacharacters even though execve should make them inert —
// defense in depth.
func TestValidateGuestName_rejects_shell_metachars(t *testing.T) {
	attacks := []string{
		"g-;rm -rf /",
		"g-$(touch /tmp/pwned)",
		"g-`touch`",
		"g-|cat /etc/passwd",
		"g->/tmp/x",
		"g- foo",
		"g-\x00null",
	}
	for _, attack := range attacks {
		t.Run(attack, func(t *testing.T) {
			if _, err := validateGuestName(attack); err == nil {
				t.Errorf("expected %q to be REJECTED (shell metachars), but it was accepted", attack)
			}
		})
	}
}

// TestValidateGuestName_rejects_bad_hyphens verifies the consecutive/
// leading/trailing hyphen rejection.
func TestValidateGuestName_rejects_bad_hyphens(t *testing.T) {
	attacks := []string{
		"-gabc",
		"gabc-",
		"g--abc",
		"-",
		"--",
	}
	for _, attack := range attacks {
		t.Run(attack, func(t *testing.T) {
			if _, err := validateGuestName(attack); err == nil {
				t.Errorf("expected %q to be REJECTED (bad hyphens), but it was accepted", attack)
			}
		})
	}
}

// TestValidateGuestName_rejects_overflow verifies length cap.
func TestValidateGuestName_rejects_overflow(t *testing.T) {
	long := "g-"
	for i := 0; i < 70; i++ {
		long += "a"
	}
	if _, err := validateGuestName(long); err == nil {
		t.Errorf("expected 70+ char name to be REJECTED, but it was accepted")
	}
}
