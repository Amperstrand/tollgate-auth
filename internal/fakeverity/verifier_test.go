package fakeverity

import (
	"errors"
	"testing"

	"tollgate-auth/internal/cashu"
)

func TestNewFakeVerifier_Defaults(t *testing.T) {
	fv := NewFakeVerifier()

	if fv.VerifyResult.OK != true {
		t.Error("default VerifyResult.OK should be true")
	}
	if fv.VerifyResult.Msg != "OK" {
		t.Error("default VerifyResult.Msg should be OK")
	}
	if fv.RedeemErr != nil {
		t.Error("default RedeemErr should be nil")
	}
	if fv.VerifyCalled != 0 {
		t.Error("default VerifyCalled should be 0")
	}
	if fv.RedeemCalled != 0 {
		t.Error("default RedeemCalled should be 0")
	}

}

func TestFakeVerifier_Verify_ReturnsConfiguredResult(t *testing.T) {
	fv := NewFakeVerifier()
	fv.VerifyResult = VerifyResult{OK: false, Msg: "Token already spent"}

	ok, msg := fv.Verify(&cashu.TokenData{Mint: "https://test.example.com", Amount: 8})
	if ok != false {
		t.Error("expected ok=false")
	}
	if msg != "Token already spent" {
		t.Errorf("msg = %q, want %q", msg, "Token already spent")
	}
	if fv.VerifyCalled != 1 {
		t.Errorf("VerifyCalled = %d, want 1", fv.VerifyCalled)
	}
	if fv.LastVerifiedTok == nil || fv.LastVerifiedTok.Amount != 8 {
		t.Error("LastVerifiedTok not captured correctly")
	}
}

func TestFakeVerifier_Verify_CounterIncrements(t *testing.T) {
	fv := NewFakeVerifier()

	fv.Verify(nil)
	fv.Verify(nil)
	fv.Verify(nil)

	if fv.VerifyCalled != 3 {
		t.Errorf("VerifyCalled = %d, want 3", fv.VerifyCalled)
	}
}

func TestFakeVerifier_Redeem_Success(t *testing.T) {
	fv := NewFakeVerifier()

	err := fv.Redeem("cashuBabc123")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if fv.RedeemCalled != 1 {
		t.Errorf("RedeemCalled = %d, want 1", fv.RedeemCalled)
	}
	if fv.LastRedeemedTok != "cashuBabc123" {
		t.Errorf("LastRedeemedTok = %q, want %q", fv.LastRedeemedTok, "cashuBabc123")
	}
}

func TestFakeVerifier_Redeem_Error(t *testing.T) {
	fv := NewFakeVerifier()
	fv.RedeemErr = errors.New("cdk-cli failed")

	err := fv.Redeem("cashuBabc")
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "cdk-cli failed" {
		t.Errorf("error = %q, want %q", err.Error(), "cdk-cli failed")
	}
}

func TestFakeReplayGuard_CheckAndMark(t *testing.T) {
	g := NewFakeReplayGuard()

	// First call: not spent → returns false, marks it
	if g.CheckAndMark("hash1") {
		t.Error("first CheckAndMark should return false (not spent)")
	}

	// Second call with same hash: already spent → returns true
	if !g.CheckAndMark("hash1") {
		t.Error("second CheckAndMark should return true (already spent)")
	}

	// Different hash: not spent
	if g.CheckAndMark("hash2") {
		t.Error("different hash should return false (not spent)")
	}

	// Verify both are in the map
	if !g.Spent["hash1"] {
		t.Error("hash1 should be in Spent map")
	}
	if !g.Spent["hash2"] {
		t.Error("hash2 should be in Spent map")
	}
}

func TestFakeReplayGuard_EmptyMap(t *testing.T) {
	g := NewFakeReplayGuard()
	if len(g.Spent) != 0 {
		t.Error("new FakeReplayGuard should have empty Spent map")
	}
}
