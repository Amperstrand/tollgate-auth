package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tollgate-auth/internal/auth"
	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/fakeverity"
	"tollgate-auth/internal/sessiond"
	"tollgate-auth/internal/testtoken"
)

func setupTestDeps(t *testing.T) (*auth.Dependencies, string) {
	t.Helper()
	tmpDir := t.TempDir()
	sessions := &auth.SessionStore{Dir: filepath.Join(tmpDir, "sessions")}
	os.MkdirAll(sessions.Dir, 0700)
	fv := fakeverity.NewFakeVerifier()
	deps := &auth.Dependencies{
		Sessions:   sessions,
		Verifier:   fv,
		OperatorID: "test-operator",
		HMACKey:    []byte("test-hmac-key-16b"),
		AuthMode:   "local",
	}
	return deps, tmpDir
}

func setupTestDepsDelegated(t *testing.T) (*auth.Dependencies, string) {
	t.Helper()
	tmpDir := t.TempDir()
	sessions := &auth.SessionStore{Dir: filepath.Join(tmpDir, "sessions")}
	os.MkdirAll(sessions.Dir, 0700)
	fv := fakeverity.NewFakeVerifier()
	bs := fakeverity.NewFakeBootstrapper()
	deps := &auth.Dependencies{
		Sessions:     sessions,
		Verifier:     fv,
		Bootstrapper: bs,
		OperatorID:   "test-operator",
		HMACKey:      []byte("test-hmac-key-16b"),
		AuthMode:     "delegated",
	}
	return deps, tmpDir
}

func TestProcessAuth_CashuTokenInUsername_Accept(t *testing.T) {
	deps, _ := setupTestDeps(t)
	token := testtoken.V4Token(8)
	result := auth.ProcessAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "", "", "")
	if !result.Accept {
		t.Fatalf("expected Accept, got: %s", result.ReplyMessage)
	}
}

func TestProcessAuth_CashuTokenInPassword_Accept(t *testing.T) {
	deps, _ := setupTestDeps(t)
	token := testtoken.V4Token(8)
	result := auth.ProcessAuth(deps, "anyuser", "aa:bb:cc:dd:ee:ff", token, "", "", "")
	if !result.Accept {
		t.Fatalf("expected Accept, got: %s", result.ReplyMessage)
	}
}

func TestProcessAuth_LNURLw_Disabled_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)
	result := auth.ProcessAuth(deps, "lnurlwdp68gup6jhjumue2nn29", "aa:bb:cc:dd:ee:ff", "", "", "", "")
	if result.Accept {
		t.Fatal("expected Reject (LNURLW disabled)")
	}
}

func TestProcessAuth_NoPaymentCredential_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)
	result := auth.ProcessAuth(deps, "user", "aa:bb:cc:dd:ee:ff", "pass", "", "", "")
	if result.Accept {
		t.Fatal("expected Reject")
	}
}

func TestProcessAuth_RedeemFails_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)
	fv := deps.Verifier.(*fakeverity.FakeVerifier)
	fv.RedeemErr = fmt.Errorf("cdk-cli failed")
	token := testtoken.V4Token(8)
	result := auth.ProcessAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "", "", "")
	if result.Accept {
		t.Fatal("expected Reject (fail-closed on redemption failure)")
	}
}

func TestProcessAuth_SpentToken_NoSession_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)
	fv := deps.Verifier.(*fakeverity.FakeVerifier)
	fv.CheckStateResult = cashu.StateSpent
	token := testtoken.V4Token(8)
	result := auth.ProcessAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "", "", "")
	if result.Accept {
		t.Fatal("expected Reject (token already spent, no active session)")
	}
}

func TestProcessAuth_SpentToken_ActiveSession_Reconnect(t *testing.T) {
	deps, _ := setupTestDeps(t)
	fv := deps.Verifier.(*fakeverity.FakeVerifier)
	fv.CheckStateResult = cashu.StateSpent
	token := testtoken.V4Token(8)
	deps.Sessions.Save(&auth.SessionRecord{
		MAC: "aa:bb:cc:dd:ee:ff", Token: "spenthash",
		Guest: "radius-test", Mint: "https://testnut.cashu.exchange",
		Amount: 8, Unit: "sat",
		Started: time.Now(), Duration: 600,
		PayType: "cashu", Class: "test-class",
	})
	result := auth.ProcessAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "", "", "")
	if !result.Accept {
		t.Fatalf("expected Accept (session reconnect), got: %s", result.ReplyMessage)
	}
}

func TestProcessAuth_PendingToken_Reject(t *testing.T) {
	deps, _ := setupTestDeps(t)
	fv := deps.Verifier.(*fakeverity.FakeVerifier)
	fv.CheckStateResult = cashu.StatePending
	token := testtoken.V4Token(8)
	result := auth.ProcessAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "", "", "")
	if result.Accept {
		t.Fatal("expected Reject (token pending)")
	}
}

func TestProcessAuth_DelegatedMode_CashuToken_Accepts(t *testing.T) {
	deps, _ := setupTestDepsDelegated(t)
	token := testtoken.V4Token(8)
	result := auth.ProcessAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "", "", "")
	if !result.Accept {
		t.Fatalf("expected Accept (delegated), got: %s", result.ReplyMessage)
	}
}

func TestProcessAuth_DelegatedMode_NoCredential_Reject(t *testing.T) {
	deps, _ := setupTestDepsDelegated(t)
	result := auth.ProcessAuth(deps, "user", "aa:bb:cc:dd:ee:ff", "pass", "", "", "")
	if result.Accept {
		t.Fatal("expected Reject (no credential in delegated mode)")
	}
}

func TestProcessAuth_DelegatedMode_BootstrapFails_Reject(t *testing.T) {
	deps, _ := setupTestDepsDelegated(t)
	bs := deps.Bootstrapper.(*fakeverity.FakeBootstrapper)
	bs.Err = fmt.Errorf("sessiond unavailable")
	token := testtoken.V4Token(8)
	result := auth.ProcessAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "", "", "")
	if result.Accept {
		t.Fatal("expected Reject (bootstrap failed)")
	}
}

func TestProcessAuth_DelegatedMode_Reconnect_ActiveSession(t *testing.T) {
	deps, _ := setupTestDepsDelegated(t)
	token := testtoken.V4Token(8)
	deps.Sessions.Save(&auth.SessionRecord{
		MAC: "aa:bb:cc:dd:ee:ff", Token: "hash",
		Guest: "radius-test", Mint: "https://testnut.cashu.exchange",
		Amount: 8, Unit: "sat",
		Started: time.Now(), Duration: 600,
		PayType: "cashu", Class: "test-class",
	})
	result := auth.ProcessAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "", "", "")
	if !result.Accept {
		t.Fatalf("expected Accept (reconnect), got: %s", result.ReplyMessage)
	}
}

// Ensure unused imports don't cause build failures
var _ = sessiond.SessionState{}
