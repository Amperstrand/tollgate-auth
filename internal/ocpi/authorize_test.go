package ocpi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHandleAuthorize_UnknownUID covers the dispatch fallback: a UID that is
// neither a known prepay record nor cashu-prefixed should be DISALLOWED with
// the "neither a prepay token nor a Cashu token" reason.
func TestHandleAuthorize_UnknownUID(t *testing.T) {
	store := NewStore()
	authz := &Authorizer{Store: store, InfoBase: "http://dash.example/"}

	rec := httptest.NewRecorder()
	authz.HandleAuthorize(rec, httptest.NewRequest(http.MethodPost, "/ocpi/emsp/2.2.1/tokens/UNKNOWN/authorize", nil), "UNKNOWN")

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	data, _ := json.Marshal(resp.Data)
	var ar AuthorizeResponse
	json.Unmarshal(data, &ar)

	if ar.Allowed != AuthzDisallowed {
		t.Errorf("unknown UID allowed = %q, want DISALLOWED", ar.Allowed)
	}
	if ar.InfoURL == "" {
		t.Error("DISALLOWED response should include info_url so driver knows where to pay")
	}

	// Authorize event should be logged.
	snap := store.Snapshot()
	if len(snap.AuthzLog) != 1 {
		t.Fatalf("authz log = %d entries, want 1", len(snap.AuthzLog))
	}
	if snap.AuthzLog[0].Allowed != AuthzDisallowed {
		t.Errorf("logged allowed = %q, want DISALLOWED", snap.AuthzLog[0].Allowed)
	}
}

// TestHandleAuthorize_PrepayAllowed verifies the primary happy path: a
// prepay record that exists and has not been used authorizes the session.
func TestHandleAuthorize_PrepayAllowed(t *testing.T) {
	store := NewStore()
	store.PutPrepay(&PrepayRecord{
		UID:            "OCPI-AB12CD34",
		CashuTokenHash: "abc12345def67890abc12345def67890abc12345def67890abc12345def67890",
		AllotmentSec:   120,
		CreditAmount:      12,
		MintURL:        "https://testnut.cashu.space",
	})
	authz := &Authorizer{Store: store, InfoBase: "http://dash.example/"}

	rec := httptest.NewRecorder()
	authz.HandleAuthorize(rec, httptest.NewRequest(http.MethodPost, "/ocpi/...", nil), "OCPI-AB12CD34")

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	data, _ := json.Marshal(resp.Data)
	var ar AuthorizeResponse
	json.Unmarshal(data, &ar)

	if ar.Allowed != AuthzAllowed {
		t.Errorf("valid prepay allowed = %q, want ALLOWED", ar.Allowed)
	}
	if ar.AuthorizationReference == "" {
		t.Error("ALLOWED response should include authorization_reference for audit")
	}

	// Authorize should mark the prepay record.
	rec2, _ := store.GetPrepay("OCPI-AB12CD34")
	if rec2.AuthorizedAt == nil {
		t.Error("prepay record should have AuthorizedAt set after successful authorize")
	}
}

// TestHandleAuthorize_PrepayAlreadyUsed verifies that a CDR-marked prepay
// record cannot be re-authorized. This is the replay protection that prevents
// a single Cashu payment from gating unlimited charging sessions.
func TestHandleAuthorize_PrepayAlreadyUsed(t *testing.T) {
	store := NewStore()
	store.PutPrepay(&PrepayRecord{
		UID:  "OCPI-USED1",
		Used: true,
	})
	authz := &Authorizer{Store: store, InfoBase: "http://dash.example/"}

	rec := httptest.NewRecorder()
	authz.HandleAuthorize(rec, httptest.NewRequest(http.MethodPost, "/ocpi/...", nil), "OCPI-USED1")

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	data, _ := json.Marshal(resp.Data)
	var ar AuthorizeResponse
	json.Unmarshal(data, &ar)

	if ar.Allowed != AuthzDisallowed {
		t.Errorf("used prepay allowed = %q, want DISALLOWED", ar.Allowed)
	}
}

// TestHandleAuthorize_PrepayStaleAuthorizedWindow verifies that a prepay
// record whose AuthorizedAt is older than the 2-minute window is rejected.
// This prevents a forgotten browser session from being replayed later.
func TestHandleAuthorize_PrepayStaleAuthorizedWindow(t *testing.T) {
	store := NewStore()
	store.PutPrepay(&PrepayRecord{UID: "OCPI-STALE1"})
	// Manually mark authorized 5 minutes ago (beyond the 2-min window).
	fiveAgo := time.Now().Add(-5 * time.Minute)
	store.mu.Lock()
	if r, ok := store.tokens["OCPI-STALE1"]; ok {
		r.AuthorizedAt = &fiveAgo
	}
	store.mu.Unlock()

	authz := &Authorizer{Store: store, InfoBase: "http://dash.example/"}
	rec := httptest.NewRecorder()
	authz.HandleAuthorize(rec, httptest.NewRequest(http.MethodPost, "/ocpi/...", nil), "OCPI-STALE1")

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	data, _ := json.Marshal(resp.Data)
	var ar AuthorizeResponse
	json.Unmarshal(data, &ar)

	if ar.Allowed != AuthzDisallowed {
		t.Errorf("stale prepay allowed = %q, want DISALLOWED", ar.Allowed)
	}
}

// TestHandleAuthorize_AuthorizeLogAppended verifies that every authorize
// call — whether ALLOWED or DISALLOWED — is appended to the audit log.
func TestHandleAuthorize_AuthorizeLogAppended(t *testing.T) {
	store := NewStore()
	authz := &Authorizer{Store: store, InfoBase: "http://dash.example/"}

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		authz.HandleAuthorize(rec, httptest.NewRequest(http.MethodPost, "/ocpi/...", nil), "UID-"+string(rune('A'+i)))
	}

	snap := store.Snapshot()
	if len(snap.AuthzLog) != 3 {
		t.Errorf("authz log = %d, want 3", len(snap.AuthzLog))
	}

	// Most-recent-first ordering.
	if snap.AuthzLog[0].UID != "UID-C" {
		t.Errorf("authz log[0].UID = %q, want UID-C (most recent first)", snap.AuthzLog[0].UID)
	}
}

// TestNewTokenA_IsUniqueAndHex verifies Token A generation produces
// 32-char hex strings (16 bytes). Two consecutive calls must differ.
func TestNewTokenA_IsUniqueAndHex(t *testing.T) {
	a := newTokenA()
	b := newTokenA()

	if len(a) != 32 {
		t.Errorf("Token A length = %d, want 32 hex chars", len(a))
	}
	for _, c := range a {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("Token A contains non-hex char %q", c)
			break
		}
	}
	if a == b {
		t.Error("two consecutive Token A generations produced identical values")
	}
}

// TestDeriveCommandsURL exercises the URL transformation used by the Sender
// to compute the CPO's commands endpoint from their version_details URL.
func TestDeriveCommandsURL(t *testing.T) {
	got, err := deriveCommandsURL(
		"https://cpo.example.com/ocpi/cpo/2.2.1/version_details",
		"START_SESSION",
		"req-001",
	)
	if err != nil {
		t.Fatalf("deriveCommandsURL: %v", err)
	}
	want := "https://cpo.example.com/ocpi/cpo/2.2.1/commands/START_SESSION/req-001"
	if got != want {
		t.Errorf("deriveCommandsURL = %q, want %q", got, want)
	}
}

func TestDeriveCommandsURL_RejectsBadInput(t *testing.T) {
	_, err := deriveCommandsURL("https://cpo.example.com/ocpi/cpo/2.2.1/credentials", "X", "Y")
	if err == nil {
		t.Error("deriveCommandsURL should reject URLs not ending in /version_details")
	}
}
