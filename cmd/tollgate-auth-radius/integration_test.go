package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"tollgate-auth/internal/auth"
	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/fakeverity"
	"tollgate-auth/internal/testtoken"
)

func setupIntegrationDeps(t *testing.T) *Dependencies {
	t.Helper()
	return &Dependencies{
		Sessions:   &SessionStore{Dir: t.TempDir()},
		Verifier:   fakeverity.NewFakeVerifier(),
		OperatorID: "test-operator",
		HMACKey:    []byte("test-hmac-key-16b"),
		AuthMode:   "local",
	}
}

func TestE2ECashuAcceptFullFlow(t *testing.T) {
	deps := setupIntegrationDeps(t)
	mac := "aa:bb:cc:dd:ee:ff"
	token := testtoken.V4Token(8)

	result := auth.ProcessAuth(deps, token, mac, "", "", "", "")

	if !result.Accept {
		t.Fatalf("expected Accept, got Reject: %s", result.ReplyMessage)
	}
	if result.SessionTimeout != 80 {
		t.Errorf("SessionTimeout = %d, want 80", result.SessionTimeout)
	}
	if !strings.Contains(result.ReplyMessage, "8 sat") {
		t.Errorf("ReplyMessage should contain '8 sat': %q", result.ReplyMessage)
	}
	if !strings.Contains(result.ReplyMessage, "1m") {
		t.Errorf("ReplyMessage should contain '1m': %q", result.ReplyMessage)
	}

	sessionPath := deps.Sessions.Path(mac)
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("session file should exist on disk: %v", err)
	}
	if len(data) == 0 {
		t.Error("session file should not be empty")
	}

	if result.Class == "" {
		t.Error("Class attribute should be non-empty on Accept")
	}
}

func TestE2ECashuReplayRejection(t *testing.T) {
	deps := setupIntegrationDeps(t)
	mac1 := "aa:bb:cc:dd:ee:ff"
	mac2 := "11:22:33:44:55:66"
	token := testtoken.V4Token(8)

	first := auth.ProcessAuth(deps, token, mac1, "", "", "", "")
	if !first.Accept {
		t.Fatalf("first call should Accept: %s", first.ReplyMessage)
	}

	deps.Verifier.(*fakeverity.FakeVerifier).CheckStateResult = cashu.StateSpent

	second := auth.ProcessAuth(deps, token, mac2, "", "", "", "")
	if second.Accept {
		t.Fatal("second call with same token (different MAC) should Reject (replay)")
	}
	if !strings.Contains(second.ReplyMessage, "already spent") {
		t.Errorf("ReplyMessage should contain 'already spent': %q", second.ReplyMessage)
	}
}

func TestE2ELNURLwAcceptFullFlow(t *testing.T) {
	deps := setupIntegrationDeps(t)

	result := auth.ProcessAuth(deps, testtoken.LNURLwCode(), "aa:bb:cc:dd:ee:ff", "", "", "", "")

	if result.Accept {
		t.Fatal("expected Reject (LNURLW disabled)")
	}
}

func TestE2ESplitTokenFullFlow(t *testing.T) {
	deps := setupIntegrationDeps(t)
	first200, rest178 := testtoken.V4TokenDLEQSplit()

	result := auth.ProcessAuth(deps, rest178, "aa:bb:cc:dd:ee:ff", first200, "", "", "")

	if !result.Accept {
		t.Fatalf("expected Accept: %s", result.ReplyMessage)
	}
	if result.SessionTimeout != 80 {
		t.Errorf("SessionTimeout = %d, want 80", result.SessionTimeout)
	}
}

func TestE2EReconnectionFlow(t *testing.T) {
	deps := setupIntegrationDeps(t)
	mac := "aa:bb:cc:dd:ee:ff"
	token := testtoken.V4Token(8)

	first := auth.ProcessAuth(deps, token, mac, "", "", "", "")
	if !first.Accept {
		t.Fatalf("first auth should Accept: %s", first.ReplyMessage)
	}

	second := auth.ProcessAuth(deps, "anything", mac, "", "", "", "")
	if !second.Accept {
		t.Fatalf("reconnection should Accept: %s", second.ReplyMessage)
	}
	if !strings.Contains(second.ReplyMessage, "remaining") {
		t.Errorf("ReplyMessage should contain 'remaining': %q", second.ReplyMessage)
	}
	if second.SessionTimeout >= first.SessionTimeout {
		t.Errorf("SessionTimeout should be < original (%d), got %d", first.SessionTimeout, second.SessionTimeout)
	}
}

func TestE2EMultipleAmounts(t *testing.T) {
	amounts := []int{1, 2, 4, 8, 16, 32, 64, 100, 500}

	for _, amount := range amounts {
		t.Run(fmt.Sprintf("%d_sat", amount), func(t *testing.T) {
			deps := setupIntegrationDeps(t)
			token := testtoken.V4Token(amount)

			result := auth.ProcessAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "", "", "")

			if !result.Accept {
				t.Fatalf("expected Accept: %s", result.ReplyMessage)
			}
			wantTimeout := amount * 10
			if result.SessionTimeout != wantTimeout {
				t.Errorf("SessionTimeout = %d, want %d", result.SessionTimeout, wantTimeout)
			}
		})
	}
}

func TestE2EOutputFormat(t *testing.T) {
	deps := setupIntegrationDeps(t)
	token := testtoken.V4Token(8)

	result := auth.ProcessAuth(deps, token, "aa:bb:cc:dd:ee:ff", "", "", "", "")

	if !result.Accept {
		t.Fatalf("expected Accept: %s", result.ReplyMessage)
	}
	if strings.Contains(result.ReplyMessage, ",") {
		t.Errorf("ReplyMessage should not contain commas: %q", result.ReplyMessage)
	}
	if strings.Contains(result.ReplyMessage, "\n") {
		t.Errorf("ReplyMessage should not contain newlines: %q", result.ReplyMessage)
	}
	if strings.Contains(result.ReplyMessage, `"`) {
		t.Errorf("ReplyMessage should not contain double quotes: %q", result.ReplyMessage)
	}
}

func TestE2ESessionExpiredThenReauth(t *testing.T) {
	deps := setupIntegrationDeps(t)
	mac1 := "aa:bb:cc:dd:ee:ff"
	mac2 := "11:22:33:44:55:66"
	token1 := testtoken.V4Token(8)

	first := auth.ProcessAuth(deps, token1, mac1, "", "", "", "")
	if !first.Accept {
		t.Fatalf("first auth should Accept: %s", first.ReplyMessage)
	}

	rec, found := deps.Sessions.Get(mac1)
	if !found {
		t.Fatal("session should exist for mac1")
	}
	rec.Started = time.Now().Add(-2 * time.Hour)
	if err := deps.Sessions.Save(rec); err != nil {
		t.Fatalf("failed to expire session: %v", err)
	}

	deps.Verifier.(*fakeverity.FakeVerifier).CheckStateResult = cashu.StateSpent

	replayResult := auth.ProcessAuth(deps, token1, mac2, "", "", "", "")
	if replayResult.Accept {
		t.Fatal("replayed token should Reject")
	}

	token2 := testtoken.V4Token(16)
	deps.Verifier.(*fakeverity.FakeVerifier).CheckStateResult = cashu.StateUnspent
	reauth := auth.ProcessAuth(deps, token2, mac1, "", "", "", "")
	if !reauth.Accept {
		t.Fatalf("new token on expired session should Accept: %s", reauth.ReplyMessage)
	}
}
