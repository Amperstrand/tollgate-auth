package ocpi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"tollgate-auth/internal/auth"
	"tollgate-auth/internal/cashu"
)

// Authorizer wraps the shared auth.Dependencies so handlers can call
// ProcessAuth for real Cashu verification. Same pipeline as SSH/RADIUS.
type Authorizer struct {
	Deps     *auth.Dependencies
	Store    *Store
	InfoBase string // base URL we advertise as info_url when blocking (dashboard)
}

// NewAuthorizer wires up the handler.
func NewAuthorizer(deps *auth.Dependencies, store *Store, infoBase string) *Authorizer {
	return &Authorizer{Deps: deps, Store: store, InfoBase: infoBase}
}

// HandleAuthorize is POST /ocpi/emsp/{version}/tokens/{uid}/authorize.
//
// UID conventions for the PoC:
//   - Raw Cashu token (cashuA…/cashuB…) — verified immediately, no prepay record.
//   - Short OCPI token UID previously issued via the dashboard — look up in store.
//
// In both cases the Cashu pipeline runs through the same internal/auth.ProcessAuth
// used by SSH and RADIUS, so all replay protection, ledger entries, and session
// state are reused unchanged.
func (a *Authorizer) HandleAuthorize(w http.ResponseWriter, r *http.Request, uid string) {
	start := time.Now()
	uid = strings.TrimSpace(uid)

	var (
		allowed = AuthzDisallowed
		reason  = "no payment credential"
		source  string
		authRef string
	)

	defer func() {
		slog.Info("ocpi authorize",
			"uid_prefix", safePrefix(uid, 16),
			"allowed", allowed,
			"reason", reason,
			"source", source,
			"elapsed_ms", time.Since(start).Milliseconds(),
		)
		a.Store.AppendAuthz(AuthorizeEvent{
			At:      start,
			UID:     uid,
			Allowed: allowed,
			Reason:  reason,
			Source:  source,
		})
		writeJSON(w, OK(AuthorizeResponse{
			Allowed:                allowed,
			AuthorizationReference: authRef,
			InfoURL:                a.InfoBase + "/",
		}))
	}()

	// Flow 1: short OCPI token UID issued by our dashboard.
	if rec, ok := a.Store.GetPrepay(uid); ok {
		source = "prepay"
		if rec.Used {
			reason = "token already used"
			return
		}
		if rec.AuthorizedAt != nil && time.Since(*rec.AuthorizedAt) > 2*time.Minute {
			reason = "stale authorize window"
			return
		}
		// Accept the prepay. The Cashu was already verified at issue time.
		a.Store.MarkPrepayAuthorized(uid)
		allowed = AuthzAllowed
		reason = fmt.Sprintf("prepay %ds allotment, %d sat from %s", rec.AllotmentSec, rec.CreditAmount, rec.MintURL)
		authRef = safePrefix(rec.CashuTokenHash, 16)
		return
	}

	// Flow 2: raw Cashu token. Verify through the shared pipeline.
	if strings.HasPrefix(uid, "cashu") {
		source = "cashu-direct"
		tokenHash := cashu.TokenHash(uid)
		shortHash := safePrefix(tokenHash, 16)
		result := auth.ProcessAuth(a.Deps, uid, shortHash, "", "", "ocpi", "")
		if result.Accept {
			allowed = AuthzAllowed
			reason = result.LogMessage
			authRef = shortHash
			return
		}
		reason = result.ReplyMessage
		return
	}

	reason = "uid is neither a prepay token nor a Cashu token"
	source = "unknown"
}

// IssuePrepay is called by the dashboard's POST /api/prepay endpoint.
// It runs the Cashu pipeline immediately, then stores an OCPI token UID
// backed by the resulting allotment. Returns the new UID.
func (a *Authorizer) IssuePrepay(cashuToken string) (*PrepayRecord, error) {
	tokenHash := cashu.TokenHash(cashuToken)
	result := auth.ProcessAuth(a.Deps, cashuToken, tokenHash[:16], "", "", "ocpi", "")
	if !result.Accept {
		return nil, fmt.Errorf("cashu verify failed: %s", result.ReplyMessage)
	}

	uid := "OCPI-" + tokenHash[:8]
	rec := &PrepayRecord{
		UID:            uid,
		CashuTokenHash: tokenHash,
		AllotmentSec:   result.SessionTimeout,
		StartedAt:      time.Now(),
		CreditAmount:      result.CreditAmount,
		MintURL:        result.MintURL,
		ContractID:     "NPC-OCPI-" + tokenHash[:8],
	}
	a.Store.PutPrepay(rec)
	slog.Info("prepay issued",
		"uid", uid,
		"allotment_sec", rec.AllotmentSec,
		"credit_amount", rec.CreditAmount,
		"mint", rec.MintURL,
	)
	return rec, nil
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, r Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(r); err != nil {
		slog.Error("write json", "error", err)
	}
}

func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// never leak full Cashu tokens in logs
	if strings.HasPrefix(s, "cashu") {
		return s[:n] + "…(redacted)"
	}
	return s[:n]
}

// readBodyCaps returns the body as a string, capped to maxBytes.
func readBodyCaps(r io.Reader, maxBytes int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxBytes))
}
