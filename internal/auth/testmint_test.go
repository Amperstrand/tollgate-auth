package auth

import "testing"

// TestIsTestMint_DefaultAllowlistIsAnchored pins the default test-mint
// allowlist (issue #25): the previous `.*` default made IsTestMint a no-op, so
// every mint — including arbitrary production or attacker mints — passed the
// allowlist gate in ProcessAuth and the fcd redemption path.
func TestIsTestMint_DefaultAllowlistIsAnchored(t *testing.T) {
	allowed := []string{
		"https://testnut.cashu.space",    // canonical (internal/testtoken.TestMint)
		"https://testnut.cashu.exchange", // observed in production session records
		"https://testnut.cashu.space/",
	}
	for _, m := range allowed {
		if !IsTestMint(m) {
			t.Errorf("IsTestMint(%q) = false, want true (known test mint)", m)
		}
	}

	denied := []string{
		"",
		"https://mint.example.com",
		"https://testnut.cashu.space.evil.example.com",
		"https://evil.example.com/testnut.cashu.space",
		"https://testnut-fake.example.com",
	}
	for _, m := range denied {
		if IsTestMint(m) {
			t.Errorf("IsTestMint(%q) = true, want false (not an allowed test mint)", m)
		}
	}
}

// The operator override must be honored when valid and fail closed to the
// anchored default when the regex is invalid — never to a permissive match.
func TestCompileTestMintPattern_OverrideAndFallback(t *testing.T) {
	custom := compileTestMintPattern(`^https://ci\.mint\.example\.net$`)
	if !custom.MatchString("https://ci.mint.example.net") {
		t.Error("valid override regex not honored")
	}
	if custom.MatchString("https://testnut.cashu.space") {
		t.Error("valid override must replace, not extend, the default allowlist")
	}

	fallback := compileTestMintPattern(`([`)
	if !fallback.MatchString("https://testnut.cashu.space") {
		t.Error("invalid override must fall back to the default allowlist")
	}
	if fallback.MatchString("https://mint.example.com") {
		t.Error("invalid override fallback must not be permissive")
	}
}
