package ocpi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleVersions_Advertises_2_2_1(t *testing.T) {
	h := &Handlers{PublicBaseURL: "https://emsp.example.com"}
	rec := httptest.NewRecorder()
	h.HandleVersions(rec, httptest.NewRequest(http.MethodGet, "/ocpi/versions", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.StatusCode != StatusSuccess {
		t.Errorf("status_code = %d, want 1000", resp.StatusCode)
	}
	data, _ := json.Marshal(resp.Data)
	var ver VersionsResponse
	if err := json.Unmarshal(data, &ver); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(ver.Versions) != 1 {
		t.Fatalf("versions count = %d, want 1", len(ver.Versions))
	}
	if ver.Versions[0].Version != "2.2.1" {
		t.Errorf("version = %q, want 2.2.1", ver.Versions[0].Version)
	}
	if !strings.Contains(ver.Versions[0].URL, "2.2.1") {
		t.Errorf("URL %q should contain 2.2.1", ver.Versions[0].URL)
	}
}

func TestHandleVersionDetails_RejectsWrongVersion(t *testing.T) {
	h := &Handlers{PublicBaseURL: "https://emsp.example.com"}
	rec := httptest.NewRecorder()
	h.HandleVersionDetails(rec, httptest.NewRequest(http.MethodGet, "/ocpi/2.0/version_details", nil), "2.0")

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.StatusCode != StatusInvalidVersion {
		t.Errorf("status_code = %d, want %d (invalid version)", resp.StatusCode, StatusInvalidVersion)
	}
}

func TestHandleVersionDetails_AdvertisesFiveReceiverEndpoints(t *testing.T) {
	h := &Handlers{PublicBaseURL: "https://emsp.example.com"}
	rec := httptest.NewRecorder()
	h.HandleVersionDetails(rec, httptest.NewRequest(http.MethodGet, "/ocpi/2.2.1/version_details", nil), "2.2.1")

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	data, _ := json.Marshal(resp.Data)
	var vd VersionDetail
	json.Unmarshal(data, &vd)

	wantModules := map[string]bool{
		"credentials": false, "locations": false, "sessions": false,
		"cdrs": false, "tokens": false,
	}
	for _, e := range vd.Endpoints {
		if _, ok := wantModules[e.Identifier]; !ok {
			t.Errorf("unexpected module %q in endpoints", e.Identifier)
		}
		wantModules[e.Identifier] = true
		if e.Role != RoleReceiver {
			t.Errorf("module %q role = %q, want RECEIVER", e.Identifier, e.Role)
		}
	}
	for mod, found := range wantModules {
		if !found {
			t.Errorf("module %q missing from endpoints", mod)
		}
	}
}

func TestHandleCredentials_HandshakeRejectsWrongTokenA(t *testing.T) {
	h := &Handlers{
		Store:         NewStore(),
		OurTokenA:     "expected-bootstrap-token",
		PublicBaseURL: "https://emsp.example.com",
	}

	req := httptest.NewRequest(http.MethodPost, "/ocpi/2.2.1/credentials",
		strings.NewReader(`{"token":"peer-b","url":"https://peer.example","party_id":"OCL","country_code":"NO"}`))
	req.Header.Set("Authorization", "Token wrong-token")
	rec := httptest.NewRecorder()

	h.HandleCredentials(rec, req)

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.StatusCode != StatusInvalidOrMissingRW {
		t.Errorf("status_code = %d, want %d (invalid bootstrap token)", resp.StatusCode, StatusInvalidOrMissingRW)
	}
	if h.Store.GetPeer() != nil {
		t.Error("peer should not be stored when Token A is wrong")
	}
}

func TestHandleCredentials_HandshakeSuccessReturnsTokenC(t *testing.T) {
	h := &Handlers{
		Store:         NewStore(),
		OurTokenA:     "expected-bootstrap-token",
		OurParty:      "TGA",
		OurCountry:    "NO",
		PublicBaseURL: "https://emsp.example.com",
	}

	req := httptest.NewRequest(http.MethodPost, "/ocpi/2.2.1/credentials",
		strings.NewReader(`{"token":"peer-b","url":"https://peer.example/ocpi/cpo/2.2.1/version_details","party_id":"OCL","country_code":"NO"}`))
	req.Header.Set("Authorization", "Token expected-bootstrap-token")
	rec := httptest.NewRecorder()

	h.HandleCredentials(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	data, _ := json.Marshal(resp.Data)
	var creds Credentials
	json.Unmarshal(data, &creds)

	if creds.Token == "" {
		t.Error("handshake response should include non-empty Token C")
	}
	if creds.Token == "peer-b" {
		t.Error("Token C should be server-generated, not echo of peer's Token B")
	}
	if creds.PartyID != "TGA" {
		t.Errorf("response party_id = %q, want TGA (ours, not peer's)", creds.PartyID)
	}

	peer := h.Store.GetPeer()
	if peer == nil {
		t.Fatal("peer not stored after handshake")
	}
	if peer.TheirParty != "OCL" || peer.TheirCountry != "NO" {
		t.Errorf("stored peer = %+v, want party=OCL country=NO", peer)
	}
	if peer.TheirTokenB != "peer-b" {
		t.Errorf("stored peer TheirTokenB = %q, want peer-b", peer.TheirTokenB)
	}
}

func TestHandleSessions_PutStores(t *testing.T) {
	h := &Handlers{Store: NewStore()}
	req := httptest.NewRequest(http.MethodPut, "/ocpi/emsp/2.2.1/sessions/sess-1",
		strings.NewReader(`{"id":"sess-1","kwh":5.5,"status":"ACTIVE","location_id":"loc-1","auth_id":"OCPI-X"}`))
	rec := httptest.NewRecorder()
	h.HandleSessions(rec, req, "sess-1")

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.StatusCode != StatusSuccess {
		t.Errorf("status_code = %d, want 1000", resp.StatusCode)
	}
	if len(h.Store.Snapshot().Sessions) != 1 {
		t.Error("session not stored")
	}
}

func TestHandleCDRs_MissingIdReturnsClientError(t *testing.T) {
	h := &Handlers{Store: NewStore()}
	req := httptest.NewRequest(http.MethodPost, "/ocpi/emsp/2.2.1/cdrs",
		strings.NewReader(`{"kwh":5.5}`))
	rec := httptest.NewRecorder()
	h.HandleCDRs(rec, req)

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.StatusCode != StatusClientError {
		t.Errorf("status_code = %d, want %d (missing CDR id)", resp.StatusCode, StatusClientError)
	}
}

func TestHandleCDRs_PostStoresAndMarksPrepayUsed(t *testing.T) {
	h := &Handlers{Store: NewStore()}
	h.Store.PutPrepay(&PrepayRecord{UID: "OCPI-A", AllotmentSec: 60})

	req := httptest.NewRequest(http.MethodPost, "/ocpi/emsp/2.2.1/cdrs",
		strings.NewReader(`{"id":"cdr-1","auth_id":"OCPI-A","kwh":3.0,"total_cost":0.5,"currency":"BTC"}`))
	rec := httptest.NewRecorder()
	h.HandleCDRs(rec, req)

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.StatusCode != StatusSuccess {
		t.Fatalf("status_code = %d, want 1000", resp.StatusCode)
	}
	rec2, _ := h.Store.GetPrepay("OCPI-A")
	if !rec2.Used {
		t.Error("CDR with auth_id matching prepay UID should mark prepay as used")
	}
}

func TestHandleCDRs_MethodNotAllowed(t *testing.T) {
	h := &Handlers{Store: NewStore()}
	req := httptest.NewRequest(http.MethodGet, "/ocpi/emsp/2.2.1/cdrs", nil)
	rec := httptest.NewRecorder()
	h.HandleCDRs(rec, req)

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.StatusCode != StatusUnknownMethod {
		t.Errorf("status_code = %d, want %d (method not allowed)", resp.StatusCode, StatusUnknownMethod)
	}
}
