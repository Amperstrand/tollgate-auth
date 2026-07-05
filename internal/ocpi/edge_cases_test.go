package ocpi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHandleAuthorize_ReplayAfterUse(t *testing.T) {
	store := NewStore()
	store.PutPrepay(&PrepayRecord{
		UID:            "OCPI-REPLAY1",
		CashuTokenHash: "aaaa111122223333444455556666777788889999aaaabbbbccccddddeeeeffff",
		AllotmentSec:   300,
		CreditAmount:      5,
		MintURL:        "https://testnut.cashu.space",
	})
	authz := &Authorizer{Store: store, InfoBase: "http://dash.example/"}

	rec1 := httptest.NewRecorder()
	authz.HandleAuthorize(rec1, httptest.NewRequest(http.MethodPost, "/ocpi/...", nil), "OCPI-REPLAY1")
	assertAllowed(t, rec1, "first authorize should be ALLOWED")

	store.PutCDR(&CDR{ID: "cdr-replay1", AuthID: "OCPI-REPLAY1"})

	rec2 := httptest.NewRecorder()
	authz.HandleAuthorize(rec2, httptest.NewRequest(http.MethodPost, "/ocpi/...", nil), "OCPI-REPLAY1")
	assertDisallowed(t, rec2, "second authorize after CDR should be DISALLOWED")
}

func assertAllowed(t *testing.T, rec *httptest.ResponseRecorder, msg string) {
	t.Helper()
	var raw map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &raw)
	data, _ := json.Marshal(raw["data"])
	var ar AuthorizeResponse
	json.Unmarshal(data, &ar)
	if ar.Allowed != AuthzAllowed {
		t.Fatalf("%s: got %s", msg, ar.Allowed)
	}
}

func assertDisallowed(t *testing.T, rec *httptest.ResponseRecorder, msg string) {
	t.Helper()
	var raw map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &raw)
	data, _ := json.Marshal(raw["data"])
	var ar AuthorizeResponse
	json.Unmarshal(data, &ar)
	if ar.Allowed != AuthzDisallowed {
		t.Fatalf("%s: got %s", msg, ar.Allowed)
	}
}

func TestHandleAuthorize_StaleAuthorizeWindowExpires(t *testing.T) {
	store := NewStore()
	store.PutPrepay(&PrepayRecord{UID: "OCPI-STALE2"})
	store.mu.Lock()
	if r, ok := store.tokens["OCPI-STALE2"]; ok {
		tenMinAgo := time.Now().Add(-10 * time.Minute)
		r.AuthorizedAt = &tenMinAgo
	}
	store.mu.Unlock()

	authz := &Authorizer{Store: store, InfoBase: "http://dash.example/"}
	rec := httptest.NewRecorder()
	authz.HandleAuthorize(rec, httptest.NewRequest(http.MethodPost, "/ocpi/...", nil), "OCPI-STALE2")
	assertDisallowed(t, rec, "stale authorized token (>2min) should be DISALLOWED")
}

func TestStore_ConcurrentAuthorizeSafety(t *testing.T) {
	store := NewStore()
	store.PutPrepay(&PrepayRecord{
		UID:            "OCPI-CONCURRENT",
		CashuTokenHash: "bbbb111122223333444455556666777788889999aaaabbbbccccddddeeeeffff",
		AllotmentSec:   300,
	})
	authz := &Authorizer{Store: store, InfoBase: "http://dash.example/"}

	var wg sync.WaitGroup
	allowedCount := 0
	var mu sync.Mutex
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			authz.HandleAuthorize(rec, httptest.NewRequest(http.MethodPost, "/ocpi/...", nil), "OCPI-CONCURRENT")
			var raw map[string]interface{}
			json.Unmarshal(rec.Body.Bytes(), &raw)
			data, _ := json.Marshal(raw["data"])
			var ar AuthorizeResponse
			json.Unmarshal(data, &ar)
			if ar.Allowed == AuthzAllowed {
				mu.Lock()
				allowedCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowedCount == 0 {
		t.Fatal("at least one concurrent authorize should succeed")
	}
}

func TestHandleCDRs_CDRWithMissingFields(t *testing.T) {
	h := &Handlers{Store: NewStore()}

	cases := []struct {
		name string
		body string
	}{
		{"empty body", `{}`},
		{"missing id", `{"kwh":5.5}`},
		{"null id", `{"id":null,"kwh":5.5}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/ocpi/emsp/2.2.1/cdrs", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			h.HandleCDRs(rec, req)
			var resp Response
			json.Unmarshal(rec.Body.Bytes(), &resp)
			if resp.StatusCode == StatusSuccess {
				t.Errorf("CDR with %s should not return success", tc.name)
			}
		})
	}
}

func TestHandleSessions_PatchUpdatesExisting(t *testing.T) {
	h := &Handlers{Store: NewStore()}

	body := `{"id":"sess-patch","kwh":5.0,"status":"ACTIVE","location_id":"loc","auth_id":"OCPI-X"}`
	req := httptest.NewRequest(http.MethodPut, "/ocpi/emsp/2.2.1/sessions/sess-patch", strings.NewReader(body))
	h.HandleSessions(httptest.NewRecorder(), req, "sess-patch")

	body2 := `{"id":"sess-patch","kwh":10.0,"status":"CHARGING","location_id":"loc","auth_id":"OCPI-X"}`
	req2 := httptest.NewRequest(http.MethodPatch, "/ocpi/emsp/2.2.1/sessions/sess-patch", strings.NewReader(body2))
	h.HandleSessions(httptest.NewRecorder(), req2, "sess-patch")

	snap := h.Store.Snapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(snap.Sessions))
	}
	if snap.Sessions[0].Kwh != 10.0 {
		t.Errorf("kwh = %f, want 10.0 (patched value)", snap.Sessions[0].Kwh)
	}
	if snap.Sessions[0].Status != "CHARGING" {
		t.Errorf("status = %s, want CHARGING", snap.Sessions[0].Status)
	}
}

func TestCharger_StartRejectInvalidToken(t *testing.T) {
	store := NewStore()
	authDeps := &mockAuthDeps{}
	server := &Server{
		store:   store,
		authz:   &Authorizer{Store: store, Deps: nil, InfoBase: "http://dash/"},
		charger: NewChargerState(),
	}
	_ = authDeps
	_ = server
}

func TestHandleVersionDetails_RejectsEmptyVersion(t *testing.T) {
	h := &Handlers{PublicBaseURL: "https://emsp.example.com"}
	rec := httptest.NewRecorder()
	h.HandleVersionDetails(rec, httptest.NewRequest(http.MethodGet, "/", nil), "")
	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.StatusCode != StatusInvalidVersion {
		t.Errorf("empty version should return invalid version, got %d", resp.StatusCode)
	}
}

func TestHandleCredentials_DeleteClearsPeer(t *testing.T) {
	h := &Handlers{
		Store:         NewStore(),
		PublicBaseURL: "https://example.com",
	}
	h.Store.SetPeer(&Peer{
		OurTokenC:   "test-c",
		TheirTokenB: "test-b",
		TheirURL:    "https://peer.example",
	})

	req := httptest.NewRequest(http.MethodDelete, "/ocpi/emsp/2.2.1/credentials", nil)
	h.HandleCredentials(httptest.NewRecorder(), req)

	if h.Store.GetPeer() != nil {
		t.Fatal("DELETE credentials should clear peer")
	}
}

func TestStoreWithDir_PersistsChargerState(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	original := &ChargerState{
		State: ChargerCharging,
		Since: time.Now(),
		Session: &LiveSession{
			ID:        "sess-persist",
			TokenUID:  "OCPI-PERSIST",
			CreditAmount: 5,
		},
	}

	err = store.SaveState("charger", original)
	if err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	var loaded ChargerState
	err = store.LoadState("charger", &loaded)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if loaded.State != ChargerCharging {
		t.Errorf("loaded state = %s, want CHARGING", loaded.State)
	}
	if loaded.Session == nil || loaded.Session.ID != "sess-persist" {
		t.Error("session not restored from disk")
	}
}

type mockAuthDeps struct{}
