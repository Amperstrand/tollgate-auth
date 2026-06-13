// Package fakeverity provides testable interfaces for Cashu token verification
// and replay protection, with fake implementations for offline testing.
package fakeverity

import (
	"tollgate-auth/internal/cashu"
)

// Verifier abstracts Cashu token operations that require network or subprocess calls.
type Verifier interface {
	// Decode decodes a Cashu token string into TokenData.
	// Delegates to cashu.DecodeToken (pure function, no network).
	Decode(tokenStr string) (*cashu.TokenData, error)

	// Verify checks token validity with the mint.
	// Production: HTTP POST to /v1/checkstate. Fake: return configurable result.
	Verify(tokenData *cashu.TokenData) (bool, string)

	// Redeem redeems a token into the wallet.
	// Production: calls cdk-cli subprocess. Fake: no-op or record call.
	Redeem(tokenStr string) error
}

// ReplayGuard abstracts replay protection for payment credentials.
type ReplayGuard interface {
	// CheckAndMark atomically checks if a hash is spent and marks it if not.
	// Returns true if the hash was already spent (caller should reject).
	CheckAndMark(thash string) bool
}

// --- Fake implementations ---

// VerifyResult configures what FakeVerifier.Verify returns.
type VerifyResult struct {
	OK  bool
	Msg string
}

// FakeVerifier is a test double for Verifier with configurable behavior
// and call tracking for assertions.
type FakeVerifier struct {
	VerifyResult VerifyResult
	RedeemErr    error

	VerifyCalled    int
	RedeemCalled    int
	LastVerifiedTok *cashu.TokenData
	LastRedeemedTok string

	// DecodeErr overrides Decode to return this error when non-nil.
	DecodeErr error
	// DecodeResult overrides Decode to return this when non-nil and DecodeErr is nil.
	DecodeResult *cashu.TokenData
}

// NewFakeVerifier returns a FakeVerifier with sensible defaults:
// Verify returns (true, "OK"), Redeem returns nil.
func NewFakeVerifier() *FakeVerifier {
	return &FakeVerifier{
		VerifyResult: VerifyResult{OK: true, Msg: "OK"},
		RedeemErr:    nil,
	}
}

// Decode decodes a Cashu token. Uses cashu.DecodeToken by default,
// or returns configured overrides.
func (f *FakeVerifier) Decode(tokenStr string) (*cashu.TokenData, error) {
	if f.DecodeErr != nil {
		return nil, f.DecodeErr
	}
	if f.DecodeResult != nil {
		return f.DecodeResult, nil
	}
	return cashu.DecodeToken(tokenStr)
}

// Verify returns the configured result and records the call.
func (f *FakeVerifier) Verify(tokenData *cashu.TokenData) (bool, string) {
	f.VerifyCalled++
	f.LastVerifiedTok = tokenData
	return f.VerifyResult.OK, f.VerifyResult.Msg
}

// Redeem returns the configured error and records the call.
func (f *FakeVerifier) Redeem(tokenStr string) error {
	f.RedeemCalled++
	f.LastRedeemedTok = tokenStr
	return f.RedeemErr
}

// FakeReplayGuard is an in-memory ReplayGuard for testing.
type FakeReplayGuard struct {
	Spent map[string]bool
}

// NewFakeReplayGuard returns a FakeReplayGuard with an empty spent map.
func NewFakeReplayGuard() *FakeReplayGuard {
	return &FakeReplayGuard{Spent: make(map[string]bool)}
}

// CheckAndMark returns true if thash was already marked spent.
// First call for a hash returns false (not spent) and marks it.
func (g *FakeReplayGuard) CheckAndMark(thash string) bool {
	if g.Spent[thash] {
		return true
	}
	g.Spent[thash] = true
	return false
}

// --- Production implementations ---

// ProductionVerifier wraps the real Cashu verification and redemption calls.
type ProductionVerifier struct {
	WalletDir string
}

// NewProductionVerifier creates a ProductionVerifier that redeems tokens
// to the given wallet directory.
func NewProductionVerifier(walletDir string) *ProductionVerifier {
	return &ProductionVerifier{WalletDir: walletDir}
}

// Decode delegates to cashu.DecodeToken.
func (p *ProductionVerifier) Decode(tokenStr string) (*cashu.TokenData, error) {
	return cashu.DecodeToken(tokenStr)
}

// Verify delegates to cashu.VerifyWithMint.
func (p *ProductionVerifier) Verify(tokenData *cashu.TokenData) (bool, string) {
	return cashu.VerifyWithMint(tokenData)
}

// Redeem delegates to cashu.RedeemToken.
func (p *ProductionVerifier) Redeem(tokenStr string) error {
	return cashu.RedeemToken(tokenStr, p.WalletDir)
}
