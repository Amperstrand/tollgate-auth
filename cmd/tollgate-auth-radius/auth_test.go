package main

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/fakeverity"
	"tollgate-auth/internal/radiusauth"

	"github.com/fxamacker/cbor/v2"
)

type testV4Token struct {
	Mint  string         `cbor:"m"`
	Unit  string         `cbor:"u"`
	Memo  string         `cbor:"d"`
	Token []testV4Entry  `cbor:"t"`
}
type testV4Entry struct {
	KeysetID []byte        `cbor:"i"`
	Proofs   []testV4Proof `cbor:"p"`
}
type testV4Proof struct {
	Amount int    `cbor:"a"`
	Secret string `cbor:"s"`
	C      []byte `cbor:"c"`
}

func encodeV4Token(v4 testV4Token) string {
	cborBytes, _ := cbor.Marshal(v4)
	encoded := base64.RawURLEncoding.EncodeToString(cborBytes)
	return "cashuB" + encoded
}

func makeTestV4Token(amount int) string {
	keysetID, _ := hex.DecodeString("00deadbeef")
	c33, _ := hex.DecodeString("02100b2c1b0f3a4d5e6f708192a3b4c5d6e7f809192a3b4c5d6e7f809192a3b4c5d")

	return encodeV4Token(testV4Token{
		Mint: "https://testnut.cashu.space",
		Unit: "sat",
		Token: []testV4Entry{
			{
				KeysetID: keysetID,
				Proofs: []testV4Proof{
					{Amount: amount, Secret: "test_secret_12345", C: c33},
				},
			},
		},
	})
}

func make378ByteToken() string {
	keysetID, _ := hex.DecodeString("00deadbeef")
	c33, _ := hex.DecodeString("02100b2c1b0f3a4d5e6f708192a3b4c5d6e7f809192a3b4c5d6e7f809192a3b4c5d")

	secret := make([]byte, 178)
	for i := range secret {
		secret[i] = 'a' + byte(i%26)
	}

	token := encodeV4Token(testV4Token{
		Mint: "https://testnut.cashu.space",
		Unit: "sat",
		Token: []testV4Entry{
			{
				KeysetID: keysetID,
				Proofs: []testV4Proof{
					{Amount: 8, Secret: string(secret), C: c33},
				},
			},
		},
	})
	if len(token) != 378 {
		panic(fmt.Sprintf("make378ByteToken: token is %d bytes, not 378", len(token)))
	}
	return token
}

func setupTestDeps(t *testing.T) (*Dependencies, string) {
	t.Helper()
	tmpDir := t.TempDir()
	sessions := &SessionStore{Dir: filepath.Join(tmpDir, "sessions")}
	os.MkdirAll(sessions.Dir, 0700)

	fv := fakeverity.NewFakeVerifier()
	rg := fakeverity.NewFakeReplayGuard()

	deps := &Dependencies{
		Sessions:   sessions,
		Replay:     rg,
		Verifier:   fv,
		OperatorID: "test-operator",
		HMACKey:    []byte("test-hmac-key-16b"),
		AuthMode:   "local",
	}
	return deps, tmpDir
}

func TestProcessAuth_CashuTokenInUsername_Accept(t *testing.T) {
	deps, _ := setupTestDeps(t)
	token := makeTestV4Token(8)

	result := processAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "")

	if !result.Accept {
		t.Fatalf("expected Accept, got Reject: %s", result.ReplyMessage)
	}
	if result.SessionTimeout != 8*60 {
		t.Errorf("SessionTimeout = %d, want %d", result.SessionTimeout, 8*60)
	}
	if result.AcctInterval != 60 {
		t.Errorf("AcctInterval = %d, want 60", result.AcctInterval)
	}
	if !strings.Contains(result.ReplyMessage, "8 sat") {
		t.Errorf("ReplyMessage should contain '8 sat': %q", result.ReplyMessage)
	}
	if deps.Verifier.(*fakeverity.FakeVerifier).VerifyCalled != 1 {
		t.Errorf("VerifyCalled = %d, want 1", deps.Verifier.(*fakeverity.FakeVerifier).VerifyCalled)
	}
	if deps.Verifier.(*fakeverity.FakeVerifier).RedeemCalled != 1 {
		t.Errorf("RedeemCalled = %d, want 1", deps.Verifier.(*fakeverity.FakeVerifier).RedeemCalled)
	}
}

func TestProcessAuth_CashuTokenInPassword_Accept(t *testing.T) {
	deps, _ := setupTestDeps(t)
	token := makeTestV4Token(16)

	result := processAuth(deps, "user1", "aa:bb:cc:dd:ee:ff", token, "")

	if !result.Accept {
		t.Fatalf("expected Accept, got Reject: %s", result.ReplyMessage)
	}
	if result.SessionTimeout != 16*60 {
		t.Errorf("SessionTimeout = %d, want %d", result.SessionTimeout, 16*60)
	}
}

func TestProcessAuth_CashuTokenInCleartextPassword_Accept(t *testing.T) {
	deps, _ := setupTestDeps(t)
	token := makeTestV4Token(4)

	result := processAuth(deps, "user1", "aa:bb:cc:dd:ee:ff", "", token)

	if !result.Accept {
		t.Fatalf("expected Accept, got Reject: %s", result.ReplyMessage)
	}
	if result.SessionTimeout != 4*60 {
		t.Errorf("SessionTimeout = %d, want %d", result.SessionTimeout, 4*60)
	}
}

func TestProcessAuth_LNURLwInUsername_Accept(t *testing.T) {
	deps, _ := setupTestDeps(t)

	result := processAuth(deps, "lnurlwdp68gup6jhjumue2nn29", "aa:bb:cc:dd:ee:ff", "", "")

	if !result.Accept {
		t.Fatalf("expected Accept, got Reject: %s", result.ReplyMessage)
	}
	if result.SessionTimeout != 3600 {
		t.Errorf("SessionTimeout = %d, want 3600", result.SessionTimeout)
	}
	if !strings.Contains(result.ReplyMessage, "LNURLw") {
		t.Errorf("ReplyMessage should contain 'LNURLw': %q", result.ReplyMessage)
	}
}

func TestProcessAuth_LNURLwInPassword_Accept(t *testing.T) {
	deps, _ := setupTestDeps(t)

	result := processAuth(deps, "user1", "aa:bb:cc:dd:ee:ff", "lnurlwdp68gup6jhjumue2nn29", "")

	if !result.Accept {
		t.Fatalf("expected Accept, got Reject: %s", result.ReplyMessage)
	}
	if result.SessionTimeout != 3600 {
		t.Errorf("SessionTimeout = %d, want 3600", result.SessionTimeout)
	}
}

func TestProcessAuth_SplitToken_Accept(t *testing.T) {
	deps, _ := setupTestDeps(t)
	fullToken := make378ByteToken()
	splitPW := fullToken[:200]
	splitUN := fullToken[200:]

	result := processAuth(deps, splitUN, "aa:bb:cc:dd:ee:ff", splitPW, "")

	if !result.Accept {
		t.Fatalf("expected Accept, got Reject: %s", result.ReplyMessage)
	}
	if result.SessionTimeout != 8*60 {
		t.Errorf("SessionTimeout = %d, want %d", result.SessionTimeout, 8*60)
	}
}

func TestProcessAuth_NoPaymentCredential_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)

	result := processAuth(deps, "alice", "aa:bb:cc:dd:ee:ff", "password123", "")

	if result.Accept {
		t.Fatal("expected Reject")
	}
	if !strings.Contains(result.ReplyMessage, "no valid payment credential") {
		t.Errorf("ReplyMessage = %q", result.ReplyMessage)
	}
}

func TestProcessAuth_InvalidMAC_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)
	token := makeTestV4Token(8)

	result := processAuth(deps, token, "../etc/passwd", "", "")

	if result.Accept {
		t.Fatal("expected Reject")
	}
	if !strings.Contains(result.ReplyMessage, "invalid session identifier") {
		t.Errorf("ReplyMessage = %q", result.ReplyMessage)
	}
}

func TestProcessAuth_TokenDecodeFailure_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)
	fv := deps.Verifier.(*fakeverity.FakeVerifier)
	fv.DecodeErr = errors.New("decode failed")

	result := processAuth(deps, "cashuBinvalid", "aa:bb:cc:dd:ee:ff", "", "")

	if result.Accept {
		t.Fatal("expected Reject")
	}
	if !strings.Contains(result.ReplyMessage, "invalid Cashu token format") {
		t.Errorf("ReplyMessage = %q", result.ReplyMessage)
	}
}

func TestProcessAuth_ZeroAmountToken_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)
	fv := deps.Verifier.(*fakeverity.FakeVerifier)
	fv.DecodeResult = &cashu.TokenData{
		Mint:   "https://testnut.cashu.space",
		Amount: 0,
	}

	result := processAuth(deps, "cashuBfake", "aa:bb:cc:dd:ee:ff", "", "")

	if result.Accept {
		t.Fatal("expected Reject")
	}
	if !strings.Contains(result.ReplyMessage, "zero value") {
		t.Errorf("ReplyMessage = %q", result.ReplyMessage)
	}
}

func TestProcessAuth_ReplayedToken_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)
	token := makeTestV4Token(8)

	thash := cashu.TokenHash(token)
	deps.Replay.(*fakeverity.FakeReplayGuard).Spent[thash] = true

	result := processAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "")

	if result.Accept {
		t.Fatal("expected Reject")
	}
	if !strings.Contains(result.ReplyMessage, "already used") {
		t.Errorf("ReplyMessage = %q", result.ReplyMessage)
	}
}

func TestProcessAuth_NonTestMint_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)
	fv := deps.Verifier.(*fakeverity.FakeVerifier)
	fv.DecodeResult = &cashu.TokenData{
		Mint:   "https://real-mint.cashu.space",
		Amount: 8,
	}

	result := processAuth(deps, "cashuBfake", "aa:bb:cc:dd:ee:ff", "", "")

	if result.Accept {
		t.Fatal("expected Reject")
	}
	if !strings.Contains(result.ReplyMessage, "not a testnet mint") {
		t.Errorf("ReplyMessage = %q", result.ReplyMessage)
	}
}

func TestProcessAuth_MintVerificationFails_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)
	fv := deps.Verifier.(*fakeverity.FakeVerifier)
	fv.VerifyResult = fakeverity.VerifyResult{OK: false, Msg: "Token already spent"}

	token := makeTestV4Token(8)
	result := processAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "")

	if result.Accept {
		t.Fatal("expected Reject")
	}
	if !strings.Contains(result.ReplyMessage, "verification failed") {
		t.Errorf("ReplyMessage = %q", result.ReplyMessage)
	}
}

func TestProcessAuth_RedeemFails_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)
	fv := deps.Verifier.(*fakeverity.FakeVerifier)
	fv.RedeemErr = errors.New("cdk-cli crashed")

	token := makeTestV4Token(8)
	result := processAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "")

	if result.Accept {
		t.Fatal("expected Reject")
	}
	if !strings.Contains(result.ReplyMessage, "redemption failed") {
		t.Errorf("ReplyMessage = %q", result.ReplyMessage)
	}
}

func TestProcessAuth_Reconnection_ActiveSession_Accept(t *testing.T) {
	deps, _ := setupTestDeps(t)
	mac := "aa:bb:cc:dd:ee:ff"

	rec := &SessionRecord{
		MAC:      mac,
		Amount:   8,
		Started:  time.Now().Add(-2 * time.Minute),
		Duration: 8 * 60,
		PayType:  radiusauth.PaymentCashu,
		Class:    "test-class",
	}
	deps.Sessions.Save(rec)

	result := processAuth(deps, "anything", mac, "", "")

	if !result.Accept {
		t.Fatalf("expected Accept for reconnection: %s", result.ReplyMessage)
	}
	if !strings.Contains(result.ReplyMessage, "resumed") {
		t.Errorf("ReplyMessage = %q", result.ReplyMessage)
	}
	if result.SessionTimeout < 1 {
		t.Errorf("SessionTimeout = %d, want >= 1", result.SessionTimeout)
	}
	if result.Class != "test-class" {
		t.Errorf("Class = %q, want 'test-class'", result.Class)
	}
}

func TestProcessAuth_Reconnection_ExpiredSession_FullAuth(t *testing.T) {
	deps, _ := setupTestDeps(t)
	mac := "aa:bb:cc:dd:ee:ff"
	token := makeTestV4Token(8)

	rec := &SessionRecord{
		MAC:      mac,
		Amount:   8,
		Started:  time.Now().Add(-20 * time.Minute),
		Duration: 8 * 60,
		PayType:  radiusauth.PaymentCashu,
	}
	deps.Sessions.Save(rec)

	result := processAuth(deps, token, mac, "", "")

	if !result.Accept {
		t.Fatalf("expected Accept: %s", result.ReplyMessage)
	}
	if !strings.Contains(result.ReplyMessage, "Valid Cashu token") {
		t.Errorf("should have run full auth flow, ReplyMessage = %q", result.ReplyMessage)
	}
}

func TestProcessAuth_Reconnection_NewToken_Topup(t *testing.T) {
	deps, _ := setupTestDeps(t)
	mac := "aa:bb:cc:dd:ee:ff"
	token := makeTestV4Token(8)

	rec := &SessionRecord{
		MAC:      mac,
		Amount:   4,
		Started:  time.Now().Add(-1 * time.Minute),
		Duration: 4 * 60,
		PayType:  radiusauth.PaymentCashu,
	}
	deps.Sessions.Save(rec)

	result := processAuth(deps, token, mac, "", "")

	if !result.Accept {
		t.Fatalf("expected Accept: %s", result.ReplyMessage)
	}
	if !strings.Contains(result.ReplyMessage, "resumed") {
		t.Errorf("active session should reconnect without re-paying: %q", result.ReplyMessage)
	}
}

func TestProcessAuth_EmptyMAC_UsernameSession(t *testing.T) {
	deps, _ := setupTestDeps(t)
	token := makeTestV4Token(8)

	result := processAuth(deps, token, "", "", "")

	if !result.Accept {
		t.Fatalf("expected Accept: %s", result.ReplyMessage)
	}
}

func TestProcessAuth_UppercaseLNURLW_Accept(t *testing.T) {
	deps, _ := setupTestDeps(t)

	result := processAuth(deps, "LNURLWDP68GUP6JHJUMUE2NN29", "aa:bb:cc:dd:ee:ff", "", "")

	if !result.Accept {
		t.Fatalf("expected Accept: %s", result.ReplyMessage)
	}
	if result.SessionTimeout != 3600 {
		t.Errorf("SessionTimeout = %d, want 3600", result.SessionTimeout)
	}
}

func TestProcessAuth_LNURLwReplay_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)
	code := "lnurlwdp68gup6jhjumue2nn29"

	thash := cashu.TokenHash(code)
	deps.Replay.(*fakeverity.FakeReplayGuard).Spent[thash] = true

	result := processAuth(deps, code, "aa:bb:cc:dd:ee:ff", "", "")

	if result.Accept {
		t.Fatal("expected Reject for replayed LNURLw")
	}
	if !strings.Contains(result.ReplyMessage, "already used") {
		t.Errorf("ReplyMessage = %q", result.ReplyMessage)
	}
}

func TestProcessAuth_SessionSavedOnAccept(t *testing.T) {
	deps, _ := setupTestDeps(t)
	mac := "aa:bb:cc:dd:ee:ff"
	token := makeTestV4Token(8)

	result := processAuth(deps, token, mac, "", "")
	if !result.Accept {
		t.Fatal("expected Accept")
	}

	rec, found := deps.Sessions.Get(mac)
	if !found {
		t.Fatal("session should be saved")
	}
	if rec.Amount != 8 {
		t.Errorf("session amount = %d, want 8", rec.Amount)
	}
	if rec.Duration != 8*60 {
		t.Errorf("session duration = %d, want %d", rec.Duration, 8*60)
	}
	if rec.PayType != radiusauth.PaymentCashu {
		t.Errorf("session paytype = %q, want cashu", rec.PayType)
	}
}

func TestProcessAuth_LNURLwSessionSavedOnAccept(t *testing.T) {
	deps, _ := setupTestDeps(t)
	mac := "aa:bb:cc:dd:ee:ff"
	code := "lnurlwdp68gup6jhjumue2nn29"

	result := processAuth(deps, code, mac, "", "")
	if !result.Accept {
		t.Fatal("expected Accept")
	}

	rec, found := deps.Sessions.Get(mac)
	if !found {
		t.Fatal("session should be saved")
	}
	if rec.Duration != 3600 {
		t.Errorf("session duration = %d, want 3600", rec.Duration)
	}
	if rec.PayType != radiusauth.PaymentLNURLW {
		t.Errorf("session paytype = %q, want lnurlw", rec.PayType)
	}
}

func TestProcessAuth_ClassAttribute_PresentOnAccept(t *testing.T) {
	deps, _ := setupTestDeps(t)
	token := makeTestV4Token(8)

	result := processAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "")

	if !result.Accept {
		t.Fatal("expected Accept")
	}
	if result.Class == "" {
		t.Error("Class should not be empty on Accept")
	}
	if !strings.HasPrefix(result.Class, "tg1:") {
		t.Errorf("Class should start with 'tg1:': %q", result.Class)
	}
}

func TestProcessAuth_DelegatedMode_NoCredential_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)
	deps.AuthMode = "delegated"

	result := processAuth(deps, "alice", "aa:bb:cc:dd:ee:ff", "password", "")

	if result.Accept {
		t.Fatal("expected Reject in delegated mode with no credential")
	}
}

func TestProcessAuth_DelegatedMode_CashuToken_Rejects(t *testing.T) {
	deps, _ := setupTestDeps(t)
	deps.AuthMode = "delegated"
	token := makeTestV4Token(8)

	result := processAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "")

	if result.Accept {
		t.Fatal("delegated cashu should be rejected by processAuth (needs sessiond)")
	}
}

func TestProcessAuth_MultipleAmounts(t *testing.T) {
	tests := []struct {
		amount     int
		wantSec    int
		wantMin    int
	}{
		{1, 60, 1},
		{8, 480, 8},
		{60, 3600, 60},
		{100, 6000, 100},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("amount_%d_sat", tc.amount), func(t *testing.T) {
			deps, _ := setupTestDeps(t)
			token := makeTestV4Token(tc.amount)

			result := processAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "")

			if !result.Accept {
				t.Fatalf("expected Accept: %s", result.ReplyMessage)
			}
			if result.SessionTimeout != tc.wantSec {
				t.Errorf("SessionTimeout = %d, want %d", result.SessionTimeout, tc.wantSec)
			}
			if !strings.Contains(result.ReplyMessage, fmt.Sprintf("%d sat", tc.amount)) {
				t.Errorf("ReplyMessage should contain '%d sat': %q", tc.amount, result.ReplyMessage)
			}
		})
	}
}

func TestProcessAuth_MixedCaseLNURLWPrefix(t *testing.T) {
	tests := []string{
		"lnurlwdp68gup6jhjumue2nn29",
		"LNURLWDP68GUP6JHJUMUE2NN29",
		"LnurlWDP68GUP6JHJUMUE2NN29",
	}

	for i, code := range tests {
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			deps, _ := setupTestDeps(t)
			result := processAuth(deps, code, "aa:bb:cc:dd:ee:ff", "", "")
			if !result.Accept {
				t.Fatalf("expected Accept for %q: %s", code, result.ReplyMessage)
			}
		})
	}
}

func TestProcessAuth_VerifyNotCalledOnDecodeFailure(t *testing.T) {
	deps, _ := setupTestDeps(t)
	fv := deps.Verifier.(*fakeverity.FakeVerifier)
	fv.DecodeErr = errors.New("bad token")

	processAuth(deps, "cashuBgarbage", "aa:bb:cc:dd:ee:ff", "", "")

	if fv.VerifyCalled != 0 {
		t.Errorf("Verify should not be called on decode failure, got %d calls", fv.VerifyCalled)
	}
	if fv.RedeemCalled != 0 {
		t.Errorf("Redeem should not be called on decode failure, got %d calls", fv.RedeemCalled)
	}
}

func TestProcessAuth_RedemptionNotCalledOnVerifyFailure(t *testing.T) {
	deps, _ := setupTestDeps(t)
	fv := deps.Verifier.(*fakeverity.FakeVerifier)
	fv.VerifyResult = fakeverity.VerifyResult{OK: false, Msg: "spent"}

	token := makeTestV4Token(8)
	processAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "")

	if fv.VerifyCalled != 1 {
		t.Errorf("Verify should be called once, got %d", fv.VerifyCalled)
	}
	if fv.RedeemCalled != 0 {
		t.Errorf("Redeem should not be called on verify failure, got %d calls", fv.RedeemCalled)
	}
}
