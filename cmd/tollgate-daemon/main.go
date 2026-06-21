// cmd/tollgate-daemon is a persistent auth server that wraps auth.ProcessAuth.
//
// Instead of spawning a new process per RADIUS auth (FreeRADIUS exec module),
// FreeRADIUS calls the lightweight tollgate-shim which connects to this daemon
// over a Unix socket. The daemon keeps the wallet warm and serves auth requests
// at ~5-20ms latency instead of ~800ms per exec invocation.
//
// Architecture:
//
//	FreeRADIUS → exec(tollgate-shim) → Unix socket → tollgate-daemon → auth.ProcessAuth
//
// The daemon also exposes HTTP endpoints for non-RADIUS auth (WireGuard, OpenVPN):
//
//	GET  /healthz   — health check
//	GET  /readyz    — readiness check (socket listener state)
//	GET  /metrics   — Prometheus-style counters
//	POST /v1/auth   — JSON auth (same protocol as socket, for VPN scripts)
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"tollgate-auth/internal/auth"
	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/config"
	"tollgate-auth/internal/fakeverity"
	"tollgate-auth/internal/operator"
	"tollgate-auth/internal/redact"
)

const (
	defaultSocketPath = "/run/tollgate/tollgate.sock"
	defaultHTTPAddr   = ":8090"
	defaultBaseDir    = "/opt/cashu-tollgate"
	defaultWalletDir  = "/var/lib/cashu-wallet"
	shutdownTimeout   = 30 * time.Second
)

// metrics holds atomic counters for observability.
type metrics struct {
	authTotal   atomic.Int64
	authAccept  atomic.Int64
	authReject  atomic.Int64
	authErrors  atomic.Int64
	authUsecs   atomic.Int64 // cumulative microseconds for avg
	uptimeStart time.Time
}

func newMetrics() *metrics {
	return &metrics{uptimeStart: time.Now()}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	socketPath := flag.String("socket", config.GetEnv("TOLLGATE_SOCKET", defaultSocketPath), "Unix socket path")
	httpAddr := flag.String("http", config.GetEnv("TOLLGATE_HTTP_ADDR", defaultHTTPAddr), "HTTP listen address")
	flag.Parse()

	// --- Load configuration ---
	baseDir := config.GetEnv("TOLLGATE_BASE_DIR", defaultBaseDir)
	walletDir := config.GetEnv("TOLLGATE_WALLET_DIR", defaultWalletDir)
	authMode := config.GetEnv("TOLLGATE_AUTH_MODE", "local")
	sessiondURL := config.GetEnv("TOLLGATE_SESSIOND_URL", "http://127.0.0.1:2121")

	// --- Initialize dependencies (once, kept warm) ---
	sessionsDir := baseDir + "/radius-sessions"
	os.MkdirAll(sessionsDir, 0700)
	sessions := &auth.SessionStore{Dir: sessionsDir}

	os.MkdirAll(baseDir, 0755)
	replay := cashu.NewReplayGuard(baseDir + "/radius-spent.txt")

	opResolver, err := operator.NewResolver("")
	if err != nil {
		slog.Error("operator resolver", "error", err)
		os.Exit(1)
	}
	opCtx := opResolver.Resolve("", "")

	verifier := fakeverity.NewProductionVerifier(walletDir)

	var bootstrapper fakeverity.Bootstrapper
	if authMode == "delegated" {
		bootstrapper = fakeverity.NewProductionBootstrapper(sessiondURL)
	}

	deps := &auth.Dependencies{
		Sessions:     sessions,
		Replay:       replay,
		Verifier:     verifier,
		Bootstrapper: bootstrapper,
		OperatorID:   opCtx.Account.ID,
		HMACKey:      opResolver.HMACKey(),
		AuthMode:     authMode,
		SessiondURL:  sessiondURL,
	}

	m := newMetrics()

	var socketReady atomic.Bool
	var authWG sync.WaitGroup

	httpServer := newHTTPServer(*httpAddr, deps, m, baseDir, *socketPath, &socketReady, &authWG)

	go func() {
		slog.Info("HTTP server listening", "addr", *httpAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server", "error", err)
			os.Exit(1)
		}
	}()

	// --- Remove stale socket ---
	if err := os.Remove(*socketPath); err != nil && !os.IsNotExist(err) {
		slog.Error("cannot remove stale socket", "socket", *socketPath, "error", err)
		os.Exit(1)
	}

	// --- Listen on Unix socket ---
	listener, err := net.Listen("unix", *socketPath)
	if err != nil {
		slog.Error("socket listen", "socket", *socketPath, "error", err)
		os.Exit(1)
	}

	// Socket permissions: freerad user needs read/write.
	os.Chmod(*socketPath, 0660)
	socketReady.Store(true)

	slog.Info("daemon starting",
		"socket", *socketPath,
		"http", *httpAddr,
		"mode", authMode,
		"operator", opCtx.Account.ID,
	)

	// --- Signal handling ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	shutdownComplete := make(chan struct{})
	go func() {
		sig := <-sigCh
		slog.Info("Shutting down gracefully...", "signal", sig.String())

		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := httpServer.Shutdown(ctx); err != nil {
			slog.Error("HTTP shutdown error", "error", err)
		}

		listener.Close()

		waitDone := make(chan struct{})
		go func() {
			authWG.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-ctx.Done():
			slog.Warn("in-flight auth requests did not complete within timeout")
		}

		os.Remove(*socketPath)
		slog.Info("Shutdown complete")
		close(shutdownComplete)
	}()

	// --- Accept loop ---
	for {
		conn, err := listener.Accept()
		if err != nil {
			if isClosedErr(err) {
				<-shutdownComplete
				return
			}
			slog.Error("accept error", "error", err)
			continue
		}
		authWG.Add(1)
		go func() {
			defer authWG.Done()
			handleSocketConn(conn, deps, m)
		}()
	}
}

func isClosedErr(err error) bool {
	return err != nil && (err == net.ErrClosed || err.Error() == "use of closed network connection")
}

// handleSocketConn handles a single Unix socket connection: read one JSON
// request, process, write one JSON response, close.
func handleSocketConn(conn net.Conn, deps *auth.Dependencies, m *metrics) {
	defer conn.Close()

	start := time.Now()
	m.authTotal.Add(1)

	// Read request (newline-delimited JSON, up to 1MB).
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scanner.Scan() {
		m.authErrors.Add(1)
		if err := scanner.Err(); err != nil {
			slog.Error("socket read", "error", err)
		}
		return
	}

	var req auth.AuthRequest
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		m.authErrors.Add(1)
		slog.Error("parse request", "error", err)
		return
	}

	// Process auth.
	result := auth.ProcessAuth(deps, req.Username, req.MAC, req.Password, req.CleartextPassword, req.NASID, req.ClientIP)

	if result.Accept {
		m.authAccept.Add(1)
	} else {
		m.authReject.Add(1)
	}

	if result.LogMessage != "" {
		slog.Info("auth detail", "message", result.LogMessage)
	}

	// Periodic session cleanup (~5% of requests).
	if time.Now().UnixNano()%20 == 0 {
		deps.Sessions.Cleanup()
	}

	// Send response.
	resp := auth.AuthResponseFromResult(result)
	data, err := json.Marshal(resp)
	if err != nil {
		m.authErrors.Add(1)
		slog.Error("marshal response", "error", err)
		return
	}
	conn.Write(data)
	conn.Write([]byte("\n"))

	elapsed := time.Since(start)
	m.authUsecs.Add(elapsed.Microseconds())

	logAuthOutcome(req.MAC, result, elapsed, deps)
}

// logAuthOutcome emits a structured slog line for an auth attempt. The
// amount_sat and mint fields are sourced from the persisted session record
// (written synchronously by auth.ProcessAuth on accept) so rejected requests
// report zero/empty values.
func logAuthOutcome(mac string, result auth.AuthResult, elapsed time.Duration, deps *auth.Dependencies) {
	outcome := "reject"
	if result.Accept {
		outcome = "accept"
	}

	amountSat := 0
	mint := ""
	if result.Accept {
		if rec, ok := deps.Sessions.Get(mac); ok && rec != nil {
			amountSat = rec.Amount
			mint = rec.Mint
		}
	}

	slog.Info("auth request",
		"outcome", outcome,
		"duration_ms", elapsed.Milliseconds(),
		"amount_sat", amountSat,
		"session_timeout", result.SessionTimeout,
		"mac", redact.LogSafe(redact.Truncate(mac, 32)),
		"mint", mint,
	)
}

// --- HTTP server ---

func newHTTPServer(addr string, deps *auth.Dependencies, m *metrics, baseDir string, socketPath string, socketReady *atomic.Bool, authWG *sync.WaitGroup) *http.Server {
	mux := http.NewServeMux()

	wgMgr := newWGManager(baseDir + "/wg-peers.json")
	wgMgr.startCleanupLoop()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`)
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !socketReady.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"status":"not ready"}`)
			return
		}
		body, _ := json.Marshal(map[string]string{
			"status": "ready",
			"socket": socketPath,
		})
		w.Write(body)
	})

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		total := m.authTotal.Load()
		accept := m.authAccept.Load()
		reject := m.authReject.Load()
		errCount := m.authErrors.Load()
		usecs := m.authUsecs.Load()
		uptime := time.Since(m.uptimeStart).Seconds()

		var avgUs float64
		if total > 0 {
			avgUs = float64(usecs) / float64(total)
		}

		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "# TYPE tollgate_auth_total counter\n")
		fmt.Fprintf(w, "tollgate_auth_total %d\n", total)
		fmt.Fprintf(w, "# TYPE tollgate_auth_accept counter\n")
		fmt.Fprintf(w, "tollgate_auth_accept %d\n", accept)
		fmt.Fprintf(w, "# TYPE tollgate_auth_reject counter\n")
		fmt.Fprintf(w, "tollgate_auth_reject %d\n", reject)
		fmt.Fprintf(w, "# TYPE tollgate_auth_errors counter\n")
		fmt.Fprintf(w, "tollgate_auth_errors %d\n", errCount)
		fmt.Fprintf(w, "# TYPE tollgate_auth_avg_duration_us gauge\n")
		fmt.Fprintf(w, "tollgate_auth_avg_duration_us %.1f\n", avgUs)
		fmt.Fprintf(w, "# TYPE tollgate_uptime_seconds gauge\n")
		fmt.Fprintf(w, "tollgate_uptime_seconds %.0f\n", uptime)
	})

	mux.HandleFunc("/v1/auth", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req auth.AuthRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}

		authWG.Add(1)
		defer authWG.Done()

		start := time.Now()
		m.authTotal.Add(1)

		result := auth.ProcessAuth(deps, req.Username, req.MAC, req.Password, req.CleartextPassword, req.NASID, req.ClientIP)

		if result.Accept {
			m.authAccept.Add(1)
		} else {
			m.authReject.Add(1)
		}

		if result.LogMessage != "" {
			slog.Info("auth detail", "message", result.LogMessage)
		}

		elapsed := time.Since(start)
		m.authUsecs.Add(elapsed.Microseconds())

		logAuthOutcome(req.MAC, result, elapsed, deps)

		resp := auth.AuthResponseFromResult(result)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	handleWGConnect(mux, deps, wgMgr)

	return &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
}
