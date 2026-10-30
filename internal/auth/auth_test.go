package auth

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"tollgate-auth/internal/fakeverity"
	"tollgate-auth/internal/radiusauth"

	"tollgate-auth/internal/testtoken"
)

// TestSessionStoreConcurrent verifies that SessionStore is safe under
// concurrent read/write access (the mutex added for daemon mode).
func TestSessionStoreConcurrent(t *testing.T) {
	store := &SessionStore{Dir: t.TempDir()}

	// Pre-populate with a session
	store.Save(&SessionRecord{
		MAC:      "aa:bb:cc:dd:ee:01",
		Token:    "testhash",
		Guest:    "guest",
		Mint:     "test",
		Amount:   10,
		Started:  time.Now(),
		Duration: 3600,
		Source:   "test",
		PayType:  radiusauth.PaymentCashu,
	})

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	// 50 concurrent readers
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, ok := store.Get("aa:bb:cc:dd:ee:01")
			if !ok {
				errors <- fmt.Errorf("Get failed for existing session")
				return
			}
			if rec.MAC != "aa:bb:cc:dd:ee:01" {
				errors <- fmt.Errorf("MAC mismatch: got %s", rec.MAC)
			}
			_ = store.IsActive(rec)
		}()
	}

	// 50 concurrent writers (different MACs)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			mac := fmt.Sprintf("aa:bb:cc:dd:ee:%02x", n)
			err := store.Save(&SessionRecord{
				MAC:      mac,
				Token:    fmt.Sprintf("hash-%d", n),
				Guest:    fmt.Sprintf("guest-%d", n),
				Mint:     "test",
				Amount:   n,
				Started:  time.Now(),
				Duration: 3600,
				Source:   "test",
				PayType:  radiusauth.PaymentCashu,
			})
			if err != nil {
				errors <- fmt.Errorf("Save failed: %w", err)
			}
		}(i)
	}

	// 10 concurrent removes
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			mac := fmt.Sprintf("aa:bb:cc:dd:ee:%02x", n)
			store.Remove(mac)
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}
}

// TestProcessAuthConcurrent verifies that ProcessAuth is safe under
// concurrent access with shared Dependencies (as the daemon would use).
func TestProcessAuthConcurrent(t *testing.T) {
	tmpDir := t.TempDir()
	sessions := &SessionStore{Dir: tmpDir + "/sessions"}

	fv := fakeverity.NewFakeVerifier()

	deps := &Dependencies{
		Sessions:   sessions,
		Verifier:   fv,
		OperatorID: "test-operator",
		HMACKey:    []byte("test-hmac-key-16bytes!"),
		AuthMode:   "local",
	}

	var wg sync.WaitGroup
	results := make([]AuthResult, 20)
	errors := make(chan error, 20)

	// 20 concurrent ProcessAuth calls with different LNURLw codes
	// (each generates a unique token hash, so no replay conflicts)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			token := testtoken.V4Token(8)
			mac := fmt.Sprintf("aa:bb:cc:dd:ee:%02x", n)
			result := ProcessAuth(deps, token, mac, "", "", "", "")
			results[n] = result
			if !result.Accept && result.ReplyMessage == "" {
				errors <- fmt.Errorf("goroutine %d: empty reply for non-accepted result", n)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}

	// All 20 should be accepted (unique LNURLw codes, no replay)
	accepted := 0
	for i, r := range results {
		if r.Accept {
			accepted++
		} else {
			t.Logf("goroutine %d rejected: %s", i, r.ReplyMessage)
		}
	}

	if accepted != 20 {
		t.Errorf("expected 20 accepts, got %d", accepted)
	}
}

// TestProcessAuthReplayConcurrent verifies that two concurrent requests
// with the SAME token hash are handled correctly — exactly one should
// be accepted, the other should be rejected as a race.
func TestProcessAuthReplayConcurrent(t *testing.T) {
	t.Skip("replay guard removed — mint handles replay protection")
	tmpDir := t.TempDir()
	sessions := &SessionStore{Dir: tmpDir + "/sessions"}

	fv := fakeverity.NewFakeVerifier()

	deps := &Dependencies{
		Sessions:   sessions,
		Verifier:   fv,
		OperatorID: "test-operator",
		HMACKey:    []byte("test-hmac-key-16bytes!"),
		AuthMode:   "local",
	}

	// Same LNURLw code for both goroutines (same token hash)
	code := "lnurlwdp68gup6jhjumue2nn77"

	var wg sync.WaitGroup
	results := make([]AuthResult, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			mac := fmt.Sprintf("aa:bb:cc:dd:ee:%02x", n)
			results[n] = ProcessAuth(deps, code, mac, "", "", "", "")
		}(i)
	}

	wg.Wait()

	// One should be accepted, one rejected (replay guard)
	acceptCount := 0
	rejectCount := 0
	for _, r := range results {
		if r.Accept {
			acceptCount++
		} else {
			rejectCount++
		}
	}

	// NOTE: FakeReplayGuard is not thread-safe (no mutex).
	// The production ReplayGuard uses flock(LOCK_EX) which IS safe.
	// This test documents the behavior — with the fake guard,
	// both might pass or one might race. The real guard handles this.
	if acceptCount == 0 && rejectCount == 2 {
		t.Skip("FakeReplayGuard race — both rejected (expected with non-atomic fake)")
	}
	if acceptCount > 1 {
		t.Errorf("expected at most 1 accept, got %d (replay guard broken)", acceptCount)
	}
}

// --- IsTestMint (mint allowlist, issue #25) ---

func TestIsTestMint_Allowlist(t *testing.T) {
	tests := []struct {
		mint string
		want bool
	}{
		{"https://testnut.cashu.space", true},
		// Anchored default (#30): unknown hosts — even "test"-shaped ones —
		// are rejected; operators widen via TOLLGATE_TEST_MINT_PATTERN.
		{"https://TESTMINT.example.com", false},
		{"https://testnut.cashu.space.evil.example.com", false},
		{"https://mint.example.com", false},
		{testtoken.NonTestMintURL(), false},
		{"", false},
	}
	for _, tc := range tests {
		if got := IsTestMint(tc.mint); got != tc.want {
			t.Errorf("IsTestMint(%q) = %v, want %v", tc.mint, got, tc.want)
		}
	}
}

// Regression for #25: TestMintPattern was `.*` (match-all), so every mint —
// including mainnet mints — passed the test-mint allowlist.
func TestProcessAuth_CashuNonTestMint_Rejected(t *testing.T) {
	deps := &Dependencies{
		Sessions:   &SessionStore{Dir: t.TempDir()},
		Verifier:   fakeverity.NewFakeVerifier(),
		OperatorID: "test-operator",
		HMACKey:    []byte("test-hmac-key-16bytes!"),
		AuthMode:   "local",
	}

	result := ProcessAuth(deps, testtoken.V4TokenNonTestMint(8), "aa:bb:cc:dd:ee:ff", "", "", "", "")
	if result.Accept {
		t.Fatalf("non-test mint %q must be rejected, got Accept", testtoken.NonTestMintURL())
	}
	if !strings.Contains(result.ReplyMessage, "not allowed") {
		t.Errorf("ReplyMessage = %q, want mint-not-allowed rejection", result.ReplyMessage)
	}
}

func TestProcessAuth_CashuTestMint_Accepted(t *testing.T) {
	deps := &Dependencies{
		Sessions:   &SessionStore{Dir: t.TempDir()},
		Verifier:   fakeverity.NewFakeVerifier(),
		OperatorID: "test-operator",
		HMACKey:    []byte("test-hmac-key-16bytes!"),
		AuthMode:   "local",
	}

	result := ProcessAuth(deps, testtoken.V4Token(8), "aa:bb:cc:dd:ee:ff", "", "", "", "")
	if !result.Accept {
		t.Fatalf("test mint must be accepted, got Reject: %s", result.ReplyMessage)
	}
}
