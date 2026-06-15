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
//	GET  /metrics   — Prometheus-style counters
//	POST /v1/auth   — JSON auth (same protocol as socket, for VPN scripts)
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
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
	defaultSocketPath = "/tmp/tollgate.sock"
	defaultHTTPAddr   = ":8090"
	defaultBaseDir    = "/opt/cashu-tollgate"
	defaultWalletDir  = "/var/lib/cashu-wallet"
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
	socketPath := flag.String("socket", config.GetEnv("TOLLGATE_SOCKET", defaultSocketPath), "Unix socket path")
	httpAddr := flag.String("http", config.GetEnv("TOLLGATE_HTTP_ADDR", defaultHTTPAddr), "HTTP listen address")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)

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
		log.Fatalf("Fatal: operator resolver: %v", err)
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

	// --- Start HTTP server ---
	go startHTTP(*httpAddr, deps, m)

	// --- Remove stale socket ---
	if err := os.Remove(*socketPath); err != nil && !os.IsNotExist(err) {
		log.Fatalf("Fatal: cannot remove stale socket %s: %v", *socketPath, err)
	}

	// --- Listen on Unix socket ---
	listener, err := net.Listen("unix", *socketPath)
	if err != nil {
		log.Fatalf("Fatal: listen %s: %v", *socketPath, err)
	}

	// Socket permissions: freerad user needs read/write.
	os.Chmod(*socketPath, 0660)

	// --- Signal handling ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("Received %s, shutting down...", sig)
		listener.Close()
		os.Remove(*socketPath)
		os.Exit(0)
	}()

	log.Printf("Tollgate daemon: socket=%s http=%s mode=%s operator=%s",
		*socketPath, *httpAddr, authMode, opCtx.Account.ID)

	// --- Accept loop ---
	for {
		conn, err := listener.Accept()
		if err != nil {
			if isClosedErr(err) {
				return
			}
			log.Printf("Accept error: %v", err)
			continue
		}
		go handleSocketConn(conn, deps, m)
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
			log.Printf("Error: socket read: %v", err)
		}
		return
	}

	var req auth.AuthRequest
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		m.authErrors.Add(1)
		log.Printf("Error: parse request: %v", err)
		return
	}

	// Process auth.
	result := auth.ProcessAuth(deps, req.Username, req.MAC, req.Password, req.CleartextPassword)

	if result.Accept {
		m.authAccept.Add(1)
	} else {
		m.authReject.Add(1)
	}

	if result.LogMessage != "" {
		log.Print(result.LogMessage)
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
		log.Printf("Error: marshal response: %v", err)
		return
	}
	conn.Write(data)
	conn.Write([]byte("\n"))

	elapsed := time.Since(start)
	m.authUsecs.Add(elapsed.Microseconds())

	log.Printf("Auth: mac=%s accept=%v duration=%v",
		redact.LogSafe(redact.Truncate(req.MAC, 32)), result.Accept, elapsed)
}

// --- HTTP server ---

func startHTTP(addr string, deps *auth.Dependencies, m *metrics) {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`)
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

		start := time.Now()
		m.authTotal.Add(1)

		result := auth.ProcessAuth(deps, req.Username, req.MAC, req.Password, req.CleartextPassword)

		if result.Accept {
			m.authAccept.Add(1)
		} else {
			m.authReject.Add(1)
		}

		if result.LogMessage != "" {
			log.Print(result.LogMessage)
		}

		m.authUsecs.Add(time.Since(start).Microseconds())

		resp := auth.AuthResponseFromResult(result)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	log.Printf("HTTP server on %s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("HTTP server: %v", err)
	}
}
