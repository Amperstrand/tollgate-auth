package ocpi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Handlers holds the receiver endpoint handlers and shared state.
type Handlers struct {
	Store *Store
	Authz *Authorizer
	// OurTokenA is the bootstrap token we accept from a new peer during
	// initial registration. After handshake, peer uses OurTokenC.
	OurTokenA string
	// Our party identity (for version_details and credentials responses).
	OurCountry string
	OurParty   string
	// PublicBaseURL is the externally-reachable URL of this OCPI receiver,
	// e.g. https://tollgate.example.com:8092 — used to build endpoint URLs.
	PublicBaseURL string
}

// newTokenA generates a 32-char hex token for the bootstrap handshake.
func newTokenA() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// moduleEndpoints builds the version_details endpoint list. These are the
// receiver interfaces we implement.
func (h *Handlers) moduleEndpoints() []ModuleEndpoint {
	base := strings.TrimRight(h.PublicBaseURL, "/") + "/ocpi/emsp/" + VersionNumber
	return []ModuleEndpoint{
		{Identifier: "credentials", URL: base + "/credentials", Role: RoleReceiver},
		{Identifier: "locations", URL: base + "/locations", Role: RoleReceiver},
		{Identifier: "sessions", URL: base + "/sessions", Role: RoleReceiver},
		{Identifier: "cdrs", URL: base + "/cdrs", Role: RoleReceiver},
		{Identifier: "tokens", URL: base + "/tokens", Role: RoleReceiver},
	}
}

// HandleVersions is GET /ocpi/versions — advertises 2.2.1.
func (h *Handlers) HandleVersions(w http.ResponseWriter, _ *http.Request) {
	url := strings.TrimRight(h.PublicBaseURL, "/") + "/ocpi/" + VersionNumber + "/version_details"
	writeJSON(w, OK(VersionsResponse{
		Versions: []Version{{Version: VersionNumber, URL: url}},
	}))
}

// HandleVersionDetails is GET /ocpi/{version}/version_details.
func (h *Handlers) HandleVersionDetails(w http.ResponseWriter, r *http.Request, version string) {
	if version != VersionNumber {
		writeJSON(w, Err(StatusInvalidVersion, "unsupported version "+version))
		return
	}
	writeJSON(w, OK(VersionDetail{Version: VersionNumber, Endpoints: h.moduleEndpoints()}))
}

// HandleCredentials handles GET/POST/PUT/DELETE on /ocpi/{version}/credentials.
//
// The OCPI handshake (when peer is CPO and we are eMSP):
//  1. Peer (CPO) sends GET /ocpi/versions → we return our version URL.
//  2. Peer (CPO) sends GET /ocpi/{version}/version_details → we return our endpoints.
//  3. Peer (CPO) sends POST /ocpi/{version}/credentials with Token A in
//     Authorization header, body containing {token, url, party_id, country_code}.
//     Token A is the pre-shared bootstrap token. We reply with our credentials
//     containing Token C, which peer uses for subsequent calls to us.
//  4. We initiate a reciprocal credentials POST to peer's URL with Token B
//     (which we generate), used for our outbound calls.
//
// For the PoC we implement steps 1-3 fully. Step 4 (reciprocal) requires
// calling peer's credentials endpoint — added once we have OCPPLab's URL.
func (h *Handlers) HandleCredentials(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getCredentials(w, r)
	case http.MethodPost:
		h.postCredentials(w, r)
	case http.MethodPut:
		h.putCredentials(w, r)
	case http.MethodDelete:
		h.deleteCredentials(w, r)
	default:
		writeJSON(w, Err(StatusUnknownMethod, "method not allowed"))
	}
}

func (h *Handlers) getCredentials(w http.ResponseWriter, _ *http.Request) {
	peer := h.Store.GetPeer()
	if peer == nil {
		writeJSON(w, Err(StatusNoMatchingElement, "no active peer"))
		return
	}
	writeJSON(w, OK(Credentials{
		Token:   peer.OurTokenC,
		URL:     h.PublicBaseURL + "/ocpi/" + VersionNumber + "/version_details",
		PartyID: h.OurParty,
		Country: h.OurCountry,
	}))
}

func (h *Handlers) postCredentials(w http.ResponseWriter, r *http.Request) {
	// Step 3 of OCPI handshake: peer posts their credentials with Token A.
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Token ")
	provided = strings.TrimSpace(provided)

	if h.OurTokenA != "" && provided != h.OurTokenA {
		slog.Warn("credentials POST with wrong Token A", "got_prefix", safePrefix(provided, 8))
		writeJSON(w, Err(StatusInvalidOrMissingRW, "invalid bootstrap token"))
		return
	}

	var in Credentials
	body, err := readBodyCaps(r.Body, 64*1024)
	if err != nil {
		writeJSON(w, Err(StatusClientError, "cannot read body"))
		return
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeJSON(w, Err(StatusClientError, "invalid credentials JSON: "+err.Error()))
		return
	}

	// Generate Token C: peer uses this for subsequent calls to us.
	tokenC := newTokenA()
	// We don't yet have peer's Token B (they'd send it later or it's in their body).
	tokenB := in.Token
	if tokenB == "" {
		tokenB = newTokenA()
	}

	peer := &Peer{
		OurTokenC:    tokenC,
		TheirTokenB:  tokenB,
		TheirURL:     in.URL,
		TheirParty:   in.PartyID,
		TheirCountry: in.Country,
		HandshakedAt: time.Now(),
	}
	h.Store.SetPeer(peer)

	slog.Info("ocpi handshake complete",
		"peer_party", in.PartyID,
		"peer_country", in.Country,
		"token_c_prefix", safePrefix(tokenC, 8),
	)

	writeJSON(w, OK(Credentials{
		Token:   tokenC,
		URL:     h.PublicBaseURL + "/ocpi/" + VersionNumber + "/version_details",
		PartyID: h.OurParty,
		Country: h.OurCountry,
	}))
}

func (h *Handlers) putCredentials(w http.ResponseWriter, r *http.Request) {
	// Peer rotates Token C. Body contains new credentials.
	var in Credentials
	body, err := readBodyCaps(r.Body, 64*1024)
	if err != nil {
		writeJSON(w, Err(StatusClientError, "cannot read body"))
		return
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeJSON(w, Err(StatusClientError, "invalid credentials JSON"))
		return
	}
	peer := h.Store.GetPeer()
	if peer == nil {
		writeJSON(w, Err(StatusNoMatchingElement, "no active peer to rotate"))
		return
	}
	peer.OurTokenC = in.Token
	h.Store.SetPeer(peer)
	writeJSON(w, OK(in))
}

func (h *Handlers) deleteCredentials(w http.ResponseWriter, _ *http.Request) {
	h.Store.SetPeer(nil)
	writeJSON(w, OK(nil))
}

// HandleSessionsPUT/PATCH is /ocpi/{version}/sessions/...
// CPO pushes session updates. We just persist and log.
func (h *Handlers) HandleSessions(w http.ResponseWriter, r *http.Request, _ string) {
	if r.Method != http.MethodPut && r.Method != http.MethodPatch {
		writeJSON(w, Err(StatusUnknownMethod, "method not allowed"))
		return
	}
	var sess Session
	body, err := readBodyCaps(r.Body, 64*1024)
	if err != nil {
		writeJSON(w, Err(StatusClientError, "cannot read body"))
		return
	}
	if err := json.Unmarshal(body, &sess); err != nil {
		writeJSON(w, Err(StatusClientError, "invalid session JSON"))
		return
	}
	if sess.ID == "" {
		writeJSON(w, Err(StatusClientError, "missing session id"))
		return
	}
	h.Store.PutSession(&sess)
	slog.Info("ocpi session update", "session_id", sess.ID, "status", sess.Status, "kwh", sess.Kwh)
	writeJSON(w, OK(nil))
}

// HandleCDRs is /ocpi/{version}/cdrs.
// CPO POSTs new CDRs. We persist and log.
func (h *Handlers) HandleCDRs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, Err(StatusUnknownMethod, "method not allowed"))
		return
	}
	var cdr CDR
	body, err := readBodyCaps(r.Body, 64*1024)
	if err != nil {
		writeJSON(w, Err(StatusClientError, "cannot read body"))
		return
	}
	if err := json.Unmarshal(body, &cdr); err != nil {
		writeJSON(w, Err(StatusClientError, "invalid CDR JSON"))
		return
	}
	if cdr.ID == "" {
		writeJSON(w, Err(StatusClientError, "missing CDR id"))
		return
	}
	h.Store.PutCDR(&cdr)
	slog.Info("ocpi CDR received",
		"cdr_id", cdr.ID,
		"auth_id", cdr.AuthID,
		"kwh", cdr.Kwh,
		"cost", cdr.TotalCost,
		"currency", cdr.Currency,
	)
	writeJSON(w, OK(nil))
}

// HandleLocations receives CPO location pushes (rare for eMSP receiver side,
// but hubs sometimes send these).
func (h *Handlers) HandleLocations(w http.ResponseWriter, r *http.Request, _ string) {
	// We don't store locations (the dashboard pulls them from the CPO side).
	// Just acknowledge.
	slog.Debug("ocpi location push ignored", "method", r.Method)
	writeJSON(w, OK(nil))
}
