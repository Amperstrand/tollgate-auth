package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/fakeverity"
	"tollgate-auth/internal/sessiond"
	"tollgate-auth/internal/testtoken"
)

// --- Fake Bootstrapper ---

type FakeBootstrapper struct {
	Result    *sessiond.SessionState
	Err       error
	Called    int
	LastToken string
	LastMac   string
}

func (f *FakeBootstrapper) Bootstrap(token string, mac string) (*sessiond.SessionState, error) {
	f.Called++
	f.LastToken = token
	f.LastMac = mac
	return f.Result, f.Err
}

// --- Test helpers ---

func setupTestSSHDeps(t *testing.T) (*SSHDependencies, *fakeverity.FakeVerifier, *fakeverity.FakeReplayGuard, *FakeBootstrapper) {
	t.Helper()
	fv := fakeverity.NewFakeVerifier()
	rg := fakeverity.NewFakeReplayGuard()
	fb := &FakeBootstrapper{
		Result: &sessiond.SessionState{AllotmentMs: 480000, Metric: "milliseconds"},
	}

	deps := &SSHDependencies{
		Replay:       rg,
		Verifier:     fv,
		Bootstrapper: fb,
		AuthMode:     "local",
	}
	return deps, fv, rg, fb
}

const testRemoteAddr = "192.168.1.50:54321"

// --- Local mode: happy path ---

func TestProcessSSHAuth_Local_ValidToken_Accept(t *testing.T) {
	deps, _, _, _ := setupTestSSHDeps(t)
	token := testtoken.V4Token(8)

	decision := processSSHAuth(deps, token, testRemoteAddr)

	if !decision.Accept {
		t.Fatalf("expected Accept, got Reject: %s", decision.Error)
	}
	if decision.Seconds != 8*10 {
		t.Errorf("Seconds = %d, want %d", decision.Seconds, 8*10)
	}
	if decision.TokenData == nil {
		t.Fatal("TokenData should not be nil on Accept")
	}
	if decision.TokenData.Amount != 8 {
		t.Errorf("TokenData.Amount = %d, want 8", decision.TokenData.Amount)
	}
}

func TestProcessSSHAuth_Local_MOTD_ContainsFields(t *testing.T) {
	deps, _, _, _ := setupTestSSHDeps(t)
	token := testtoken.V4Token(8)

	decision := processSSHAuth(deps, token, testRemoteAddr)

	if !decision.Accept {
		t.Fatalf("expected Accept: %s", decision.Error)
	}
	if !strings.Contains(decision.MOTD, "testnut.cashu.space") {
		t.Errorf("MOTD should contain mint URL: %q", decision.MOTD)
	}
	if !strings.Contains(decision.MOTD, "8 sat") {
		t.Errorf("MOTD should contain '8 sat': %q", decision.MOTD)
	}
	if !strings.Contains(decision.MOTD, "80 sec") {
		t.Errorf("MOTD should contain '480 sec': %q", decision.MOTD)
	}
	if !strings.Contains(decision.MOTD, decision.Guest) {
		t.Errorf("MOTD should contain guest %q: %q", decision.Guest, decision.MOTD)
	}
}

func TestProcessSSHAuth_Local_TokenHash_Set(t *testing.T) {
	deps, _, _, _ := setupTestSSHDeps(t)
	token := testtoken.V4Token(8)

	decision := processSSHAuth(deps, token, testRemoteAddr)

	if !decision.Accept {
		t.Fatalf("expected Accept: %s", decision.Error)
	}
	expectedHash := cashu.TokenHash(token)
	if decision.TokenHash != expectedHash {
		t.Errorf("TokenHash = %q, want %q", decision.TokenHash, expectedHash)
	}
}

func TestProcessSSHAuth_Local_GuestFormat(t *testing.T) {
	deps, _, _, _ := setupTestSSHDeps(t)
	token := testtoken.V4Token(8)

	decision := processSSHAuth(deps, token, testRemoteAddr)

	if !decision.Accept {
		t.Fatalf("expected Accept: %s", decision.Error)
	}
	if !strings.HasPrefix(decision.Guest, "g-") {
		t.Errorf("Guest should start with 'g-': %q", decision.Guest)
	}
	if len(decision.Guest) != 10 {
		t.Errorf("Guest should be 'g-' + 8 chars = 10 total, got %d: %q", len(decision.Guest), decision.Guest)
	}
}

func TestProcessSSHAuth_Local_VerifyCalledWithTokenData(t *testing.T) {
	deps, fv, _, _ := setupTestSSHDeps(t)
	token := testtoken.V4Token(16)

	decision := processSSHAuth(deps, token, testRemoteAddr)

	if !decision.Accept {
		t.Fatalf("expected Accept: %s", decision.Error)
	}
	if fv.VerifyCalled != 1 {
		t.Errorf("VerifyCalled = %d, want 1", fv.VerifyCalled)
	}
	if fv.LastVerifiedTok == nil || fv.LastVerifiedTok.Amount != 16 {
		t.Errorf("LastVerifiedTok Amount = %v, want 16", fv.LastVerifiedTok)
	}
}

func TestProcessSSHAuth_Local_RedeemCalledWithToken(t *testing.T) {
	deps, fv, _, _ := setupTestSSHDeps(t)
	token := testtoken.V4Token(8)

	decision := processSSHAuth(deps, token, testRemoteAddr)

	if !decision.Accept {
		t.Fatalf("expected Accept: %s", decision.Error)
	}
	if fv.RedeemCalled != 1 {
		t.Errorf("RedeemCalled = %d, want 1", fv.RedeemCalled)
	}
	if fv.LastRedeemedTok != token {
		t.Errorf("LastRedeemedTok does not match input token")
	}
}

func TestProcessSSHAuth_Local_MOTD_TestMintFlag(t *testing.T) {
	deps, _, _, _ := setupTestSSHDeps(t)
	token := testtoken.V4Token(8)

	decision := processSSHAuth(deps, token, testRemoteAddr)

	if !decision.Accept {
		t.Fatalf("expected Accept: %s", decision.Error)
	}
	if !strings.Contains(decision.MOTD, "Test:   YES") {
		t.Errorf("MOTD should show Test=YES for testnut mint: %q", decision.MOTD)
	}
}

func TestProcessSSHAuth_Local_MOTD_NonTestMintFlag(t *testing.T) {
	deps, _, _, _ := setupTestSSHDeps(t)
	token := testtoken.V4TokenWithMint(8, "https://real-mint.example.com")

	decision := processSSHAuth(deps, token, testRemoteAddr)

	if !decision.Accept {
		t.Fatalf("expected Accept (SSH doesn't check isTestMint): %s", decision.Error)
	}
	if !strings.Contains(decision.MOTD, "Test:   NO") {
		t.Errorf("MOTD should show Test=NO for non-test mint: %q", decision.MOTD)
	}
}

// --- Local mode: rejection ---

func TestProcessSSHAuth_Local_InvalidToken_Reject(t *testing.T) {
	deps, _, _, _ := setupTestSSHDeps(t)

	decision := processSSHAuth(deps, "this-is-garbage", testRemoteAddr)

	if decision.Accept {
		t.Fatal("expected Reject for garbage input")
	}
	if decision.TokenData != nil {
		t.Error("TokenData should be nil on decode failure")
	}
}

func TestProcessSSHAuth_Local_WrongPrefix_Reject(t *testing.T) {
	deps, _, _, _ := setupTestSSHDeps(t)

	decision := processSSHAuth(deps, "cashuXsomedata", testRemoteAddr)

	if decision.Accept {
		t.Fatal("expected Reject for wrong prefix")
	}
}

func TestProcessSSHAuth_Local_ZeroAmount_Reject(t *testing.T) {
	deps, fv, _, _ := setupTestSSHDeps(t)
	fv.DecodeResult = &cashu.TokenData{
		Mint:   "https://testnut.cashu.space",
		Amount: 0,
		Unit:   "sat",
	}

	decision := processSSHAuth(deps, "cashuBfake", testRemoteAddr)

	if decision.Accept {
		t.Fatal("expected Reject for zero amount")
	}
	if !strings.Contains(decision.Error, "zero value") {
		t.Errorf("Error = %q, want 'zero value'", decision.Error)
	}
}

func TestProcessSSHAuth_Local_NegativeAmount_Reject(t *testing.T) {
	deps, fv, _, _ := setupTestSSHDeps(t)
	fv.DecodeResult = &cashu.TokenData{
		Mint:   "https://testnut.cashu.space",
		Amount: -5,
		Unit:   "sat",
	}

	decision := processSSHAuth(deps, "cashuBfake", testRemoteAddr)

	if decision.Accept {
		t.Fatal("expected Reject for negative amount")
	}
}

func TestProcessSSHAuth_Local_AlreadySpent_Reject(t *testing.T) {
	deps, _, rg, _ := setupTestSSHDeps(t)
	token := testtoken.V4Token(8)

	thash := cashu.TokenHash(token)
	rg.Spent[thash] = true

	decision := processSSHAuth(deps, token, testRemoteAddr)

	if decision.Accept {
		t.Fatal("expected Reject for already-spent token")
	}
	if !strings.Contains(decision.Error, "already used") {
		t.Errorf("Error = %q, want 'already used'", decision.Error)
	}
}

func TestProcessSSHAuth_Local_VerifyFails_Reject(t *testing.T) {
	deps, fv, _, _ := setupTestSSHDeps(t)
	fv.VerifyResult = fakeverity.VerifyResult{OK: false, Msg: "proofs already spent at mint"}

	token := testtoken.V4Token(8)
	decision := processSSHAuth(deps, token, testRemoteAddr)

	if decision.Accept {
		t.Fatal("expected Reject when verify fails")
	}
	if !strings.Contains(decision.Error, "proofs already spent") {
		t.Errorf("Error = %q, should contain verify msg", decision.Error)
	}
}

func TestProcessSSHAuth_Local_RedeemFails_Reject(t *testing.T) {
	deps, fv, _, _ := setupTestSSHDeps(t)
	fv.RedeemErr = errors.New("cdk-cli crashed")

	token := testtoken.V4Token(8)
	decision := processSSHAuth(deps, token, testRemoteAddr)

	if decision.Accept {
		t.Fatal("expected Reject when redeem fails")
	}
	if !strings.Contains(decision.Error, "redemption failed") {
		t.Errorf("Error = %q, want 'redemption failed'", decision.Error)
	}
}

// --- Local mode: ordering assertions ---

func TestProcessSSHAuth_VerifyNotCalledOnDecodeFailure(t *testing.T) {
	deps, fv, _, _ := setupTestSSHDeps(t)

	processSSHAuth(deps, "garbage", testRemoteAddr)

	if fv.VerifyCalled != 0 {
		t.Errorf("Verify should not be called on decode failure, got %d", fv.VerifyCalled)
	}
	if fv.RedeemCalled != 0 {
		t.Errorf("Redeem should not be called on decode failure, got %d", fv.RedeemCalled)
	}
}

func TestProcessSSHAuth_RedeemNotCalledOnVerifyFailure(t *testing.T) {
	deps, fv, _, _ := setupTestSSHDeps(t)
	fv.VerifyResult = fakeverity.VerifyResult{OK: false, Msg: "spent"}

	token := testtoken.V4Token(8)
	processSSHAuth(deps, token, testRemoteAddr)

	if fv.VerifyCalled != 1 {
		t.Errorf("Verify should be called once, got %d", fv.VerifyCalled)
	}
	if fv.RedeemCalled != 0 {
		t.Errorf("Redeem should not be called on verify failure, got %d", fv.RedeemCalled)
	}
}

func TestProcessSSHAuth_VerifyNotCalledOnZeroAmount(t *testing.T) {
	deps, fv, _, _ := setupTestSSHDeps(t)
	fv.DecodeResult = &cashu.TokenData{
		Mint:   "https://testnut.cashu.space",
		Amount: 0,
		Unit:   "sat",
	}

	processSSHAuth(deps, "cashuBfake", testRemoteAddr)

	if fv.VerifyCalled != 0 {
		t.Errorf("Verify should not be called on zero amount, got %d", fv.VerifyCalled)
	}
}

func TestProcessSSHAuth_ReplayCheckedBeforeVerify(t *testing.T) {
	deps, _, rg, _ := setupTestSSHDeps(t)
	token := testtoken.V4Token(8)

	thash := cashu.TokenHash(token)
	rg.Spent[thash] = true

	deps.Verifier.(*fakeverity.FakeVerifier).VerifyResult = fakeverity.VerifyResult{OK: false, Msg: "should not reach"}

	decision := processSSHAuth(deps, token, testRemoteAddr)

	if decision.Accept {
		t.Fatal("expected Reject for replay")
	}
	fv := deps.Verifier.(*fakeverity.FakeVerifier)
	if fv.VerifyCalled != 0 {
		t.Errorf("Verify should not be called when replay rejects, got %d", fv.VerifyCalled)
	}
}

// --- Delegated mode: happy path ---

func TestProcessSSHAuth_Delegated_ValidToken_Accept(t *testing.T) {
	deps, _, _, fb := setupTestSSHDeps(t)
	deps.AuthMode = "delegated"
	fb.Result = &sessiond.SessionState{AllotmentMs: 300000, Metric: "milliseconds"}

	token := testtoken.V4Token(8)
	decision := processSSHAuth(deps, token, testRemoteAddr)

	if !decision.Accept {
		t.Fatalf("expected Accept: %s", decision.Error)
	}
	if decision.Seconds != 300 {
		t.Errorf("Seconds = %d, want 300 (300000ms / 1000)", decision.Seconds)
	}
}

func TestProcessSSHAuth_Delegated_BootstrapCalledWithCorrectArgs(t *testing.T) {
	deps, _, _, fb := setupTestSSHDeps(t)
	deps.AuthMode = "delegated"
	fb.Result = &sessiond.SessionState{AllotmentMs: 480000, Metric: "milliseconds"}

	token := testtoken.V4Token(8)
	processSSHAuth(deps, token, testRemoteAddr)

	if fb.Called != 1 {
		t.Errorf("Bootstrap Called = %d, want 1", fb.Called)
	}
	if fb.LastToken != token {
		t.Error("Bootstrap should receive the raw token string")
	}
	if fb.LastMac != "ssh:192.168.1.50" {
		t.Errorf("Bootstrap Mac = %q, want 'ssh:192.168.1.50'", fb.LastMac)
	}
}

func TestProcessSSHAuth_Delegated_MOTDRendered(t *testing.T) {
	deps, _, _, fb := setupTestSSHDeps(t)
	deps.AuthMode = "delegated"
	fb.Result = &sessiond.SessionState{AllotmentMs: 480000, Metric: "milliseconds"}

	token := testtoken.V4Token(8)
	decision := processSSHAuth(deps, token, testRemoteAddr)

	if !decision.Accept {
		t.Fatalf("expected Accept: %s", decision.Error)
	}
	if decision.MOTD == "" {
		t.Error("MOTD should be rendered in delegated mode")
	}
}

// --- Delegated mode: rejection ---

func TestProcessSSHAuth_Delegated_BootstrapError_Reject(t *testing.T) {
	deps, _, _, fb := setupTestSSHDeps(t)
	deps.AuthMode = "delegated"
	fb.Err = errors.New("server unreachable")

	token := testtoken.V4Token(8)
	decision := processSSHAuth(deps, token, testRemoteAddr)

	if decision.Accept {
		t.Fatal("expected Reject on bootstrap error")
	}
	if !strings.Contains(decision.Error, "delegated session failed") {
		t.Errorf("Error = %q, want 'delegated session failed'", decision.Error)
	}
}

func TestProcessSSHAuth_Delegated_ZeroAllotment_Reject(t *testing.T) {
	deps, _, _, fb := setupTestSSHDeps(t)
	deps.AuthMode = "delegated"
	fb.Result = &sessiond.SessionState{AllotmentMs: 0}

	token := testtoken.V4Token(8)
	decision := processSSHAuth(deps, token, testRemoteAddr)

	if decision.Accept {
		t.Fatal("expected Reject for zero allotment")
	}
	if !strings.Contains(decision.Error, "zero allotment") {
		t.Errorf("Error = %q, want 'zero allotment'", decision.Error)
	}
}

func TestProcessSSHAuth_Delegated_AlreadySpent_Reject(t *testing.T) {
	deps, _, rg, fb := setupTestSSHDeps(t)
	deps.AuthMode = "delegated"
	fb.Result = &sessiond.SessionState{AllotmentMs: 480000}

	token := testtoken.V4Token(8)
	thash := cashu.TokenHash(token)
	rg.Spent[thash] = true

	decision := processSSHAuth(deps, token, testRemoteAddr)

	if decision.Accept {
		t.Fatal("expected Reject for replayed token")
	}
	if fb.Called != 0 {
		t.Errorf("Bootstrap should not be called on replay, got %d", fb.Called)
	}
}

// --- Table-driven tests ---

func TestProcessSSHAuth_Local_MultipleAmounts(t *testing.T) {
	tests := []struct {
		amount  int
		wantSec int
	}{
		{1, 10},
		{8, 80},
		{30, 300},
		{60, 600},
		{100, 1000},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("amount_%d_sat", tc.amount), func(t *testing.T) {
			deps, _, _, _ := setupTestSSHDeps(t)
			token := testtoken.V4Token(tc.amount)

			decision := processSSHAuth(deps, token, testRemoteAddr)

			if !decision.Accept {
				t.Fatalf("expected Accept: %s", decision.Error)
			}
			if decision.Seconds != tc.wantSec {
				t.Errorf("Seconds = %d, want %d", decision.Seconds, tc.wantSec)
			}
		})
	}
}

func TestProcessSSHAuth_Delegated_MultipleAllotments(t *testing.T) {
	tests := []struct {
		allotmentMs uint64
		wantSec     int
	}{
		{60000, 60},
		{480000, 480},
		{3600000, 3600},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("allotment_%dms", tc.allotmentMs), func(t *testing.T) {
			deps, _, _, fb := setupTestSSHDeps(t)
			deps.AuthMode = "delegated"
			fb.Result = &sessiond.SessionState{AllotmentMs: tc.allotmentMs, Metric: "milliseconds"}

			token := testtoken.V4Token(8)
			decision := processSSHAuth(deps, token, testRemoteAddr)

			if !decision.Accept {
				t.Fatalf("expected Accept: %s", decision.Error)
			}
			if decision.Seconds != tc.wantSec {
				t.Errorf("Seconds = %d, want %d", decision.Seconds, tc.wantSec)
			}
		})
	}
}
