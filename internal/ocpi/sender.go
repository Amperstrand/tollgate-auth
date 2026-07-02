package ocpi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Sender calls CPO-side endpoints: commands (START_SESSION, STOP_SESSION) and
// location pulls. Auth uses peer's stored Token B in the Authorization header.
type Sender struct {
	store      *Store
	httpClient *http.Client
	ourCountry string
	ourParty   string
	// OurCallbackBase is the URL the CPO should call back with async command
	// results, e.g. "https://emsp.example.com/ocpi/emsp/2.2.1/commands".
	OurCallbackBase string
}

// NewSender wires the sender. callbackBase is where we want the CPO to POST
// async CommandResponse results. country/party populate OCPI-From-* routing
// headers on outbound calls.
func NewSender(store *Store, callbackBase, country, party string) *Sender {
	return &Sender{
		store:           store,
		httpClient:      &http.Client{Timeout: 30 * time.Second},
		ourCountry:      country,
		ourParty:        party,
		OurCallbackBase: strings.TrimRight(callbackBase, "/"),
	}
}

// CommandResult is the OCPI CommandResponse DTO.
type CommandResult struct {
	Result  string `json:"result"`            // ACCEPTED, REJECTED, UNKNOWN, NOT_SUPPORTED
	Timeout int32  `json:"timeout,omitempty"` // seconds to wait for async result
	Message string `json:"message,omitempty"`
}

// StartSessionCommand is the body eMSP POSTs to
// /ocpi/cpo/2.2.1/commands/START_SESSION/{request_id}.
type StartSessionCommand struct {
	ResponseURL string `json:"response_url"`
	Token       Token  `json:"token"`
	LocationID  string `json:"location_id"`
	EvseUID     string `json:"evse_uid,omitempty"`
}

// StopSessionCommand is the body eMSP POSTs to
// /ocpi/cpo/2.2.1/commands/STOP_SESSION/{request_id}.
type StopSessionCommand struct {
	ResponseURL string `json:"response_url"`
	SessionID   string `json:"session_id"`
}

// StartSession tells the CPO to start a session at the given EVSE using the
// provided token. CPO responds synchronously, then POSTs the final outcome to
// response_url asynchronously.
func (s *Sender) StartSession(ctx context.Context, requestID string, tok Token, locationID, evseUID string) (*CommandResult, error) {
	body := StartSessionCommand{
		ResponseURL: fmt.Sprintf("%s/START_SESSION/%s", s.OurCallbackBase, requestID),
		Token:       tok,
		LocationID:  locationID,
		EvseUID:     evseUID,
	}
	return s.callCommand(ctx, "START_SESSION", requestID, body)
}

// StopSession tells the CPO to stop a session by OCPI session ID.
func (s *Sender) StopSession(ctx context.Context, requestID, sessionID string) (*CommandResult, error) {
	body := StopSessionCommand{
		ResponseURL: fmt.Sprintf("%s/STOP_SESSION/%s", s.OurCallbackBase, requestID),
		SessionID:   sessionID,
	}
	return s.callCommand(ctx, "STOP_SESSION", requestID, body)
}

// callCommand POSTs to /ocpi/cpo/{version}/commands/{name}/{requestID} on the
// peer's advertised commands endpoint.
func (s *Sender) callCommand(ctx context.Context, name, requestID string, body any) (*CommandResult, error) {
	peer := s.store.GetPeer()
	if peer == nil {
		return nil, fmt.Errorf("no peer: complete credentials handshake first")
	}
	if peer.TheirURL == "" {
		return nil, fmt.Errorf("peer has no version_details URL")
	}

	cmdURL, err := deriveCommandsURL(peer.TheirURL, name, requestID)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cmdURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token "+peer.TheirTokenB)
	req.Header.Set("OCPI-From-Country-Code", s.ourCountry)
	req.Header.Set("OCPI-From-Party-ID", s.ourParty)

	slog.Info("ocpi sender",
		"command", name,
		"request_id", requestID,
		"url", cmdURL,
		"peer_token_prefix", safePrefix(peer.TheirTokenB, 8),
	)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http POST: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("CPO returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var env struct {
		StatusCode StatusCode     `json:"status_code"`
		StatusMsg  string         `json:"status_message"`
		Data       *CommandResult `json:"data"`
	}
	if err := json.Unmarshal(respBody, &env); err != nil {
		return nil, fmt.Errorf("decode response: %w (body=%q)", err, string(respBody))
	}
	if env.StatusCode != StatusSuccess {
		return env.Data, fmt.Errorf("OCPI status %d: %s", env.StatusCode, env.StatusMsg)
	}
	return env.Data, nil
}

// deriveCommandsURL converts a peer version_details URL like
// "https://cpo.example.com/ocpi/cpo/2.2.1/version_details" into a commands URL
// like "https://cpo.example.com/ocpi/cpo/2.2.1/commands/START_SESSION/{id}".
func deriveCommandsURL(versionDetailsURL, commandName, requestID string) (string, error) {
	idx := strings.LastIndex(versionDetailsURL, "/version_details")
	if idx < 0 {
		return "", fmt.Errorf("peer URL %q does not end with /version_details", versionDetailsURL)
	}
	base := versionDetailsURL[:idx]
	return fmt.Sprintf("%s/commands/%s/%s", base, commandName, requestID), nil
}

// HandleCommandCallback is POST /ocpi/emsp/2.2.1/commands/{name}/{requestID}.
// The CPO POSTs the async result of a command we initiated.
func (s *Server) HandleCommandCallback(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 {
		writeJSON(w, Err(StatusClientError, "expected /commands/{name}/{requestID}"))
		return
	}
	name := parts[0]
	requestID := parts[1]

	body, err := io.ReadAll(io.LimitReader(r.Body, 32*1024))
	if err != nil {
		writeJSON(w, Err(StatusClientError, "cannot read body"))
		return
	}
	var result CommandResult
	if err := json.Unmarshal(body, &result); err != nil {
		writeJSON(w, Err(StatusClientError, "invalid CommandResponse JSON"))
		return
	}

	slog.Info("ocpi async command result",
		"command", name,
		"request_id", requestID,
		"result", result.Result,
		"message", result.Message,
	)
	writeJSON(w, OK(nil))
}
