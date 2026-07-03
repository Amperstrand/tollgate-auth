package ocpi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"tollgate-auth/internal/auth"
	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/ledger"
)

// ChargerState simulates a physical EV charger for demo purposes.
// State machine: AVAILABLE → CHARGING → AVAILABLE (or BLOCKED on failed payment).
type ChargerState struct {
	mu      sync.Mutex
	State   string       `json:"state"` // AVAILABLE, CHARGING, BLOCKED
	Since   time.Time    `json:"since"`
	Session *LiveSession `json:"session,omitempty"`
}

type LiveSession struct {
	ID           string    `json:"id"`
	StartedAt    time.Time `json:"started_at"`
	TokenUID     string    `json:"token_uid"`
	TokenHash    string    `json:"token_hash"`
	AmountSat    int       `json:"amount_sat"`
	AllotmentSec int       `json:"allotment_sec"`
	PowerKw      float64   `json:"power_kw"`
}

const (
	ChargerAvailable = "AVAILABLE"
	ChargerCharging  = "CHARGING"
	ChargerBlocked   = "BLOCKED"
	DefaultPowerKw   = 7.4
	PricePerKwhNok   = 2.50
)

func NewChargerState() *ChargerState {
	return &ChargerState{
		State: ChargerAvailable,
		Since: time.Now(),
	}
}

// ChargeRequest is the body for POST /api/charger/start.
type ChargeRequest struct {
	CashuToken string `json:"cashu_token"`
}

// ChargeResponse is returned by start and status endpoints.
type ChargeResponse struct {
	State   string       `json:"state"`
	Session *LiveSession `json:"session,omitempty"`
	Kwh     float64      `json:"kwh,omitempty"`
	Reason  string       `json:"reason,omitempty"`
}

// HandleChargeStart takes a Cashu token directly (no prepay indirection),
// verifies it through the shared auth pipeline, and starts the virtual charger.
func (s *Server) HandleChargeStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, Err(StatusUnknownMethod, "POST required"))
		return
	}

	var body ChargeRequest
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 32*1024))
	if err != nil {
		writeJSON(w, Err(StatusClientError, "cannot read body"))
		return
	}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		writeJSON(w, Err(StatusClientError, "invalid JSON"))
		return
	}
	if body.CashuToken == "" {
		writeJSON(w, Err(StatusClientError, "missing cashu_token"))
		return
	}

	s.charger.mu.Lock()
	if s.charger.State == ChargerCharging {
		s.charger.mu.Unlock()
		writeJSON(w, Err(StatusClientError, "charger already in use — stop current session first"))
		return
	}
	s.charger.mu.Unlock()

	tokenHash := cashu.TokenHash(body.CashuToken)
	result := auth.ProcessAuth(
		s.authz.Deps,
		body.CashuToken,
		safePrefix(tokenHash, 16),
		"", "", "ocpi-charger", "",
	)

	slog.Info("charge start",
		"accept", result.Accept,
		"amount_sat", result.AmountSat,
		"allotment_sec", result.SessionTimeout,
		"token_hash_prefix", tokenHash[:8],
	)

	if !result.Accept {
		slog.Warn("charge rejected", "reason", result.ReplyMessage, "token_prefix", tokenHash[:8])
		writeJSON(w, Err(StatusClientError, result.ReplyMessage))
		return
	}

	sessionID := "sess-" + tokenHash[:8]
	session := &LiveSession{
		ID:           sessionID,
		StartedAt:    time.Now(),
		TokenUID:     "cashu-direct",
		TokenHash:    tokenHash,
		AmountSat:    result.AmountSat,
		AllotmentSec: result.SessionTimeout,
		PowerKw:      DefaultPowerKw,
	}

	s.charger.mu.Lock()
	s.charger.State = ChargerCharging
	s.charger.Since = time.Now()
	s.charger.Session = session
	s.charger.mu.Unlock()

	if s.ledger != nil {
		s.ledger.RecordAuth(ledger.LedgerEntry{
			EventType:    ledger.EventAuthAccept,
			MAC:          sessionID,
			PaymentType:  result.PayType,
			AmountSat:    result.AmountSat,
			DurationSec:  result.SessionTimeout,
			MintURL:      result.MintURL,
			TokenHash:    tokenHash,
			SessionClass: "ocpi",
			NASID:        "virtual-charger-001",
		})
	}

	writeJSON(w, OK(ChargeResponse{
		State:   ChargerCharging,
		Session: session,
	}))
}

// HandleChargeStop ends the current session, calculates kWh, generates a CDR.
func (s *Server) HandleChargeStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, Err(StatusUnknownMethod, "POST required"))
		return
	}

	s.charger.mu.Lock()
	session := s.charger.Session

	if session != nil && s.charger.State == ChargerCharging {
		elapsed := time.Since(session.StartedAt)
		kwh := session.PowerKw * elapsed.Hours()
		costNok := kwh * PricePerKwhNok

		cdr := &CDR{
			ID:          "cdr-" + session.ID,
			Started:     session.StartedAt,
			Stopped:     time.Now(),
			AuthID:      session.TokenUID,
			LocationID:  "virtual-charger-001",
			EvseUID:     "EVSE-001",
			ConnectorID: "1",
			Kwh:         kwh,
			TotalCost:   costNok,
			Currency:    "NOK",
			LastUpdated: time.Now(),
		}

		s.store.PutCDR(cdr)
		s.store.PutSession(&Session{
			ID:          session.ID,
			Started:     session.StartedAt,
			Stopped:     &cdr.Stopped,
			Kwh:         kwh,
			AuthID:      session.TokenUID,
			LocationID:  "virtual-charger-001",
			Status:      "COMPLETED",
			LastUpdated: time.Now(),
		})

		slog.Info("charge stop", "session_id", session.ID, "elapsed_sec", int(elapsed.Seconds()), "kwh", kwh, "cost_nok", costNok)

		s.charger.State = ChargerAvailable
		s.charger.Since = time.Now()
		s.charger.Session = nil
		s.charger.mu.Unlock()

		writeJSON(w, OK(ChargeResponse{State: ChargerAvailable, Kwh: kwh}))
		return
	}

	s.charger.State = ChargerAvailable
	s.charger.Since = time.Now()
	s.charger.Session = nil
	s.charger.mu.Unlock()

	writeJSON(w, OK(ChargeResponse{State: ChargerAvailable}))
}

// HandleChargeStatus returns the current charger state + live kWh if charging.
func (s *Server) HandleChargeStatus(w http.ResponseWriter, _ *http.Request) {
	s.charger.mu.Lock()
	defer s.charger.mu.Unlock()

	resp := ChargeResponse{State: s.charger.State}
	if s.charger.State == ChargerCharging && s.charger.Session != nil {
		resp.Session = s.charger.Session
		resp.Kwh = s.charger.Session.PowerKw * time.Since(s.charger.Session.StartedAt).Hours()
	}
	writeJSON(w, OK(resp))
}
