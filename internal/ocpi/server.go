package ocpi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tollgate-auth/internal/auth"
	"tollgate-auth/internal/ledger"
)

// Config holds the runtime config for the OCPI server.
type Config struct {
	ListenAddr     string // ":8092"
	PublicBaseURL  string // "https://tollgate.example.com:8092" — advertised in version_details
	OurCountry     string // ISO 3166-1 alpha-2, e.g. "NO"
	OurParty       string // ISO 15118 eMA ID party, e.g. "TGA"
	BootstrapToken string // Token A; if empty, any token accepted (PoC only)
	DashboardBase  string // base URL advertised to drivers in authorize info_url
	// DataDir, when set, enables file-backed persistence for the in-memory
	// store (CDRs, prepay records, authorize log, charger state). When empty,
	// NewServer falls back to $TOLLGATE_BASE_DIR/ocpi-state; if that is also
	// unset the store is in-memory only and state is lost on restart.
	DataDir string
}

// Server is the OCPI 2.2.1 eMSP receiver + dashboard HTTP server.
type Server struct {
	cfg      Config
	store    *Store
	authz    *Authorizer
	sender   *Sender
	handlers *Handlers
	charger  *ChargerState
	ledger   *ledger.Ledger
	httpSrv  *http.Server
}

func NewServer(cfg Config, authDeps *auth.Dependencies) *Server {
	return NewServerWithLedger(cfg, authDeps, nil)
}

func NewServerWithLedger(cfg Config, authDeps *auth.Dependencies, lg *ledger.Ledger) *Server {
	store := newPersistentStore(cfg.DataDir)
	authz := NewAuthorizer(authDeps, store, cfg.DashboardBase)
	sender := NewSender(store, cfg.PublicBaseURL+"/ocpi/emsp/"+VersionNumber+"/commands", cfg.OurCountry, cfg.OurParty)
	charger := NewChargerState()
	if err := store.LoadState(stateCharger, charger); err != nil {
		slog.Warn("ocpi charger state load failed; using defaults", "error", err)
	}
	h := &Handlers{
		Store:         store,
		Authz:         authz,
		OurTokenA:     cfg.BootstrapToken,
		OurCountry:    cfg.OurCountry,
		OurParty:      cfg.OurParty,
		PublicBaseURL: cfg.PublicBaseURL,
		Ledger:        lg,
	}
	return &Server{cfg: cfg, store: store, authz: authz, sender: sender, handlers: h, charger: charger, ledger: lg}
}

// newPersistentStore resolves the on-disk data directory and returns a store
// backed by it. Resolution order: explicit cfg.DataDir, then
// $TOLLGATE_BASE_DIR/ocpi-state, then in-memory only. A failure to open the
// directory degrades to an in-memory store with a logged warning so the server
// still boots.
func newPersistentStore(dataDir string) *Store {
	if dataDir == "" {
		if base := os.Getenv("TOLLGATE_BASE_DIR"); base != "" {
			dataDir = filepath.Join(base, "ocpi-state")
		}
	}
	if dataDir == "" {
		return NewStore()
	}
	s, err := NewStoreWithDir(dataDir)
	if err != nil {
		slog.Error("ocpi persistent store init failed; falling back to in-memory",
			"dir", dataDir, "error", err)
		return NewStore()
	}
	slog.Info("ocpi persistent store loaded", "dir", dataDir)
	return s
}

func (s *Server) Sender() *Sender { return s.sender } // Start boots the HTTP server. Blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	s.httpSrv = &http.Server{
		Addr:         s.cfg.ListenAddr,
		Handler:      s.loggingMiddleware(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("ocpi server starting", "addr", s.cfg.ListenAddr, "base_url", s.cfg.PublicBaseURL)
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.httpSrv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// loggingMiddleware logs each request at info level.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, r)
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// registerRoutes mounts all routes. Longest-prefix matching means /ocpi/versions
// and /ocpi/{version}/... patterns don't conflict.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// OCPI receiver endpoints
	mux.HandleFunc("/ocpi/versions", s.handlers.HandleVersions)
	mux.HandleFunc("/ocpi/", s.routeOCPIByVersion)

	// Dashboard & API
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/about", s.handleAbout)
	mux.HandleFunc("/api/snapshot", s.handleSnapshot)
	mux.HandleFunc("/api/prepay", s.handlePrepay)
	mux.HandleFunc("/api/charger/start", s.withChargerPersist(s.HandleChargeStart))
	mux.HandleFunc("/api/charger/stop", s.withChargerPersist(s.HandleChargeStop))
	mux.HandleFunc("/api/charger/status", s.HandleChargeStatus)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

// routeOCPIByVersion dispatches /ocpi/{role}/{version}/... to the right module.
// The role segment (emsp, cpo, hub) is part of the URL but doesn't change
// routing on our side — we only advertise emsp receiver endpoints.
func (s *Server) routeOCPIByVersion(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/ocpi/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 || parts[0] == "" {
		writeJSON(w, Err(StatusClientError, "missing role/version/module"))
		return
	}
	first := parts[0]
	tail := parts[1]

	// Skip the role segment (emsp/cpo/hub) if present.
	if first == "emsp" || first == "cpo" || first == "hub" {
		roleParts := strings.SplitN(tail, "/", 2)
		if len(roleParts) < 2 {
			writeJSON(w, Err(StatusClientError, "missing version/module after role"))
			return
		}
		first = roleParts[0]
		tail = roleParts[1]
	}

	version := first
	if version != VersionNumber {
		writeJSON(w, Err(StatusInvalidVersion, "unsupported version "+version))
		return
	}

	modParts := strings.SplitN(tail, "/", 2)
	module := modParts[0]
	rest2 := ""
	if len(modParts) > 1 {
		rest2 = modParts[1]
	}

	switch module {
	case "version_details":
		s.handlers.HandleVersionDetails(w, r, version)
	case "credentials":
		s.handlers.HandleCredentials(w, r)
	case "sessions":
		s.handlers.HandleSessions(w, r, rest2)
	case "cdrs":
		s.handlers.HandleCDRs(w, r)
	case "locations":
		s.handlers.HandleLocations(w, r, rest2)
	case "tokens":
		s.routeTokens(w, r, rest2)
	case "commands":
		s.HandleCommandCallback(w, r, rest2)
	default:
		writeJSON(w, Err(StatusNotImplemented, "unknown module "+module))
	}
}

// routeTokens handles /tokens/{uid}/authorize and PUT/PATCH /tokens/{cc}/{pid}/{uid}.
func (s *Server) routeTokens(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(rest, "/")
	if len(parts) == 2 && parts[1] == "authorize" {
		s.authz.HandleAuthorize(w, r, parts[0])
		return
	}
	// Token push from peer: not implemented in PoC, acknowledge.
	slog.Debug("ocpi tokens route", "rest", rest, "method", r.Method)
	writeJSON(w, OK(nil))
}

// withChargerPersist runs the charger handler, then durably snapshots the
// charger state to disk so a restart can resume it. The state is marshaled
// under s.charger.mu so concurrent status reads cannot observe a torn value.
// Status reads (GET /api/charger/status) are not wrapped: they do not mutate.
func (s *Server) withChargerPersist(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		s.charger.mu.Lock()
		err := s.store.SaveState(stateCharger, s.charger)
		s.charger.mu.Unlock()
		if err != nil {
			slog.Warn("ocpi charger state persist failed", "error", err)
		}
	}
}

// --- dashboard & API ---

func (s *Server) handleSnapshot(w http.ResponseWriter, _ *http.Request) {
	snap := s.store.Snapshot()
	writeJSON(w, OK(snap))
}

func (s *Server) handlePrepay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, Err(StatusUnknownMethod, "POST required"))
		return
	}
	var body struct {
		CashuToken string `json:"cashu_token"`
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 32*1024))
	if err != nil {
		writeJSON(w, Err(StatusClientError, "cannot read body"))
		return
	}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		writeJSON(w, Err(StatusClientError, "invalid JSON: "+err.Error()))
		return
	}
	if body.CashuToken == "" {
		writeJSON(w, Err(StatusClientError, "missing cashu_token"))
		return
	}
	rec, err := s.authz.IssuePrepay(body.CashuToken)
	if err != nil {
		slog.Warn("prepay failed", "error", err)
		writeJSON(w, Err(StatusClientError, err.Error()))
		return
	}
	writeJSON(w, OK(rec))
}
