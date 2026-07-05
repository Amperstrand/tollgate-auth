// Package fakeverity provides testable interfaces for Cashu token verification
// and replay protection, with fake implementations for offline testing.
package fakeverity

import (
	"sync"

	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/sessiond"
)

type Verifier interface {
	Decode(tokenStr string) (*cashu.TokenData, error)
	Verify(tokenData *cashu.TokenData) (bool, string)
	Redeem(tokenStr string) error
	CheckState(tokenData *cashu.TokenData) (cashu.ProofState, string)
}

type ReplayGuard interface {
	CheckAndMark(thash string) bool
	IsSpent(thash string) bool
}

type BootstrapResult struct {
	AllotmentMs            uint64
	CreditAmount              uint64
	EffectiveRateSecPerSat uint64
}

type Bootstrapper interface {
	Bootstrap(token string, sessionID string) (*BootstrapResult, error)
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

	// CheckStateResult controls what CheckState returns. Defaults to UNSPENT.
	CheckStateResult cashu.ProofState
}

// NewFakeVerifier returns a FakeVerifier with sensible defaults:
// Verify returns (true, "OK"), Redeem returns nil.
func NewFakeVerifier() *FakeVerifier {
	return &FakeVerifier{
		VerifyResult:     VerifyResult{OK: true, Msg: "OK"},
		RedeemErr:        nil,
		CheckStateResult: cashu.StateUnspent,
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

func (f *FakeVerifier) CheckState(tokenData *cashu.TokenData) (cashu.ProofState, string) {
	if f.CheckStateResult == "" {
		return cashu.StateUnspent, "OK"
	}
	return f.CheckStateResult, "configured"
}

// FakeReplayGuard is an in-memory ReplayGuard for testing.
type FakeReplayGuard struct {
	mu    sync.Mutex
	Spent map[string]bool
}

// NewFakeReplayGuard returns a FakeReplayGuard with an empty spent map.
func NewFakeReplayGuard() *FakeReplayGuard {
	return &FakeReplayGuard{Spent: make(map[string]bool)}
}

// CheckAndMark returns true if thash was already marked spent.
// First call for a hash returns false (not spent) and marks it.
func (g *FakeReplayGuard) CheckAndMark(thash string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Spent[thash] {
		return true
	}
	g.Spent[thash] = true
	return false
}

func (g *FakeReplayGuard) IsSpent(thash string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Spent[thash]
}

// FakeBootstrapper is a test double for Bootstrapper with configurable behavior
// and call tracking for assertions.
type FakeBootstrapper struct {
	Result      *BootstrapResult
	Err         error
	Called      int
	LastToken   string
	LastSession string
}

func NewFakeBootstrapper() *FakeBootstrapper {
	return &FakeBootstrapper{
		Result: &BootstrapResult{AllotmentMs: 80000, CreditAmount: 8, EffectiveRateSecPerSat: 10},
	}
}

func (f *FakeBootstrapper) Bootstrap(token string, sessionID string) (*BootstrapResult, error) {
	f.Called++
	f.LastToken = token
	f.LastSession = sessionID
	return f.Result, f.Err
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

func (p *ProductionVerifier) CheckState(tokenData *cashu.TokenData) (cashu.ProofState, string) {
	return cashu.CheckTokenState(tokenData)
}

type ProductionBootstrapper struct {
	BaseURL string
}

func NewProductionBootstrapper(baseURL string) *ProductionBootstrapper {
	return &ProductionBootstrapper{BaseURL: baseURL}
}

func (p *ProductionBootstrapper) Bootstrap(token string, sessionID string) (*BootstrapResult, error) {
	client := sessiond.NewClient(p.BaseURL)
	state, err := client.Bootstrap(token, sessionID)
	if err != nil {
		return nil, err
	}
	return &BootstrapResult{
		AllotmentMs:            state.AllotmentMs,
		CreditAmount:              state.CreditAmount,
		EffectiveRateSecPerSat: state.EffectiveRateSecPerSat,
	}, nil
}
