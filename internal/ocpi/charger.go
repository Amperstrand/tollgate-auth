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
	CreditAmount int       `json:"credit_amount"`
	Unit         string    `json:"unit"`
	MaxKwh       float64   `json:"max_kwh"`
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
	CashuToken   string `json:"cashu_token"`
	ProviderNpub string `json:"provider_npub,omitempty"`
}

// ChargeResponse is returned by start and status endpoints.
type ChargeResponse struct {
	State           string       `json:"state"`
	Session         *LiveSession `json:"session,omitempty"`
	Kwh             float64      `json:"kwh,omitempty"`
	CreditUsed      float64      `json:"credit_used,omitempty"`
	CreditRemaining float64      `json:"credit_remaining,omitempty"`
	Reason          string       `json:"reason,omitempty"`
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

	maxKwh := MaxKwh(result.CreditAmount, result.Unit)
	estSeconds := int(maxKwh / DefaultPowerKw * 3600)
	if estSeconds < 1 {
		estSeconds = 1
	}

	slog.Info("charge start",
		"accept", result.Accept,
		"credit", result.CreditAmount,
		"unit", result.Unit,
		"max_kwh", maxKwh,
		"est_sec", estSeconds,
		"token_hash_prefix", tokenHash[:8],
	)

	if !result.Accept {
		slog.Warn("charge rejected", "reason", result.ReplyMessage, "token_prefix", tokenHash[:8])
		writeJSON(w, Err(StatusClientError, result.ReplyMessage))
		return
	}

	if body.ProviderNpub != "" {
		tp, found := s.providers.GetTokenProvider(tokenHash)
		if !found {
			slog.Warn("charge rejected: token not bound to any provider", "charger_provider", body.ProviderNpub, "token_prefix", tokenHash[:8])
			writeJSON(w, Err(StatusClientError, "Rejected: token was not issued for this provider"))
			return
		}
		if tp.ProviderNpub != body.ProviderNpub {
			slog.Warn("charge rejected: provider mismatch",
				"token_provider", tp.ProviderNpub,
				"charger_provider", body.ProviderNpub,
				"token_prefix", tokenHash[:8],
			)
			writeJSON(w, Err(StatusClientError, "Rejected: token issued for a different provider"))
			return
		}
	}

	sessionID := "sess-" + tokenHash[:8]
	session := &LiveSession{
		ID:           sessionID,
		StartedAt:    time.Now(),
		TokenUID:     "cashu-direct",
		TokenHash:    tokenHash,
		CreditAmount: result.CreditAmount,
		Unit:         result.Unit,
		MaxKwh:       maxKwh,
		AllotmentSec: estSeconds,
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
			CreditAmount: result.CreditAmount,
			Unit:         result.Unit,
			DurationSec:  estSeconds,
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

// finishCharging — caller must hold s.charger.mu.
func (s *Server) finishCharging() (kwh, creditUsed, creditRemaining float64) {
	session := s.charger.Session
	if session == nil || s.charger.State != ChargerCharging {
		return 0, 0, 0
	}

	elapsed := time.Since(session.StartedAt)
	kwh = session.PowerKw * elapsed.Hours()
	creditUsed = kwh * PriceForUnit(session.Unit)
	creditRemaining = float64(session.CreditAmount) - creditUsed
	if creditRemaining < 0 {
		creditRemaining = 0
	}
	costNok := kwh * PricePerKwhNok
	stoppedAt := time.Now()

	cdr := &CDR{
		ID:           "cdr-" + session.ID,
		Started:      session.StartedAt,
		Stopped:      stoppedAt,
		AuthID:       session.TokenUID,
		LocationID:   "virtual-charger-001",
		EvseUID:      "EVSE-001",
		ConnectorID:  "1",
		Kwh:          kwh,
		TotalCost:    costNok,
		Currency:     "NOK",
		CreditAmount: session.CreditAmount,
		CreditUsed:   creditUsed,
		Unit:         session.Unit,
		LastUpdated:  stoppedAt,
	}
	s.store.PutCDR(cdr)
	s.store.PutSession(&Session{
		ID:           session.ID,
		Started:      session.StartedAt,
		Stopped:      &stoppedAt,
		Kwh:          kwh,
		AuthID:       session.TokenUID,
		LocationID:   "virtual-charger-001",
		Status:       "COMPLETED",
		CreditAmount: session.CreditAmount,
		Unit:         session.Unit,
		LastUpdated:  stoppedAt,
	})

	slog.Info("charge stop",
		"session_id", session.ID,
		"elapsed_sec", int(elapsed.Seconds()),
		"kwh", kwh,
		"cost_nok", costNok,
		"credit_used", creditUsed,
		"credit_remaining", creditRemaining,
		"unit", session.Unit,
	)

	s.charger.State = ChargerAvailable
	s.charger.Since = time.Now()
	s.charger.Session = nil
	return kwh, creditUsed, creditRemaining
}

// HandleChargeStop ends the current session, calculates kWh, generates a CDR.
func (s *Server) HandleChargeStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, Err(StatusUnknownMethod, "POST required"))
		return
	}

	s.charger.mu.Lock()
	kwh, creditUsed, creditRemaining := s.finishCharging()
	s.charger.mu.Unlock()

	writeJSON(w, OK(ChargeResponse{
		State:           ChargerAvailable,
		Kwh:             kwh,
		CreditUsed:      creditUsed,
		CreditRemaining: creditRemaining,
	}))
}

// HandleChargeStatus returns the current charger state + live kWh if charging.
// Auto-stops when credit balance reaches zero.
func (s *Server) HandleChargeStatus(w http.ResponseWriter, _ *http.Request) {
	s.charger.mu.Lock()
	defer s.charger.mu.Unlock()

	resp := ChargeResponse{State: s.charger.State}
	if s.charger.State == ChargerCharging && s.charger.Session != nil {
		sess := s.charger.Session
		resp.Session = sess
		resp.Kwh = sess.PowerKw * time.Since(sess.StartedAt).Hours()
		resp.CreditUsed = resp.Kwh * PriceForUnit(sess.Unit)
		resp.CreditRemaining = float64(sess.CreditAmount) - resp.CreditUsed

		if resp.CreditRemaining <= 0 {
			kwh, creditUsed, _ := s.finishCharging()
			resp.State = ChargerAvailable
			resp.Session = nil
			resp.Kwh = kwh
			resp.CreditUsed = creditUsed
			resp.CreditRemaining = 0
		}
	}
	writeJSON(w, OK(resp))
}
