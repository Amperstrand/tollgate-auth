package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"tollgate-auth/internal/auth"
)

const (
	wgSubnet      = "10.200.0"
	wgServerIP    = wgSubnet + ".1"
	wgClientStart = 2
	wgClientEnd   = 254
	wgDNS         = "1.1.1.1"
	wgIfName      = "wg0"
	wgPort        = "51820"
)

type wgPeer struct {
	IP         string `json:"ip"`
	TokenHash  string `json:"token_hash"`
	ExpiresAt  int64  `json:"expires_at"`
	AssignedAt int64  `json:"assigned_at"`
}

type wgManager struct {
	mu        sync.Mutex
	peers     map[string]*wgPeer
	stateFile string
	nextIP    int
}

func newWGManager(stateFile string) *wgManager {
	m := &wgManager{
		peers:     make(map[string]*wgPeer),
		stateFile: stateFile,
		nextIP:    wgClientStart,
	}
	m.loadState()
	return m
}

func (m *wgManager) loadState() {
	data, err := os.ReadFile(m.stateFile)
	if err != nil {
		return
	}
	json.Unmarshal(data, &m.peers)
	now := time.Now().Unix()
	highest := wgClientStart - 1
	for pk, p := range m.peers {
		if p.ExpiresAt < now {
			delete(m.peers, pk)
			continue
		}
		ipNum := lastOctet(p.IP)
		if ipNum > highest {
			highest = ipNum
		}
	}
	m.nextIP = highest + 1
}

func lastOctet(ip string) int {
	var n int
	fmt.Sscanf(ip, wgSubnet+".%d", &n)
	return n
}

func (m *wgManager) saveState() {
	data, _ := json.MarshalIndent(m.peers, "", "  ")
	os.WriteFile(m.stateFile, data, 0600)
}

func (m *wgManager) assignIP() string {
	ip := fmt.Sprintf("%s.%d", wgSubnet, m.nextIP)
	m.nextIP++
	if m.nextIP > wgClientEnd {
		m.nextIP = wgClientStart
	}
	return ip
}

func (m *wgManager) addPeer(pubkey, tokenHash string, durationSec int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.peers[pubkey]; ok && existing.ExpiresAt > time.Now().Unix() {
		if err := wgRemovePeer(pubkey); err != nil {
			slog.Warn("wg remove old peer", "pubkey", truncatePubkey(pubkey), "error", err)
		}
	}

	ip := m.assignIP()
	now := time.Now().Unix()
	peer := &wgPeer{
		IP:         ip,
		TokenHash:  tokenHash,
		ExpiresAt:  now + int64(durationSec),
		AssignedAt: now,
	}

	if err := wgAddPeer(pubkey, ip); err != nil {
		return "", fmt.Errorf("wg set peer: %w", err)
	}

	m.peers[pubkey] = peer
	m.saveState()
	return ip, nil
}

func (m *wgManager) removePeer(pubkey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := wgRemovePeer(pubkey); err != nil {
		slog.Warn("wg remove peer", "pubkey", truncatePubkey(pubkey), "error", err)
	}
	delete(m.peers, pubkey)
	m.saveState()
	return nil
}

func (m *wgManager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().Unix()
	for pubkey, peer := range m.peers {
		if peer.ExpiresAt < now {
			if err := wgRemovePeer(pubkey); err != nil {
				slog.Warn("wg cleanup remove", "pubkey", truncatePubkey(pubkey), "ip", peer.IP, "error", err)
			} else {
				slog.Info("wg cleanup removed expired peer",
					"action", "removed",
					"pubkey", truncatePubkey(pubkey),
					"ip", peer.IP,
				)
			}
			delete(m.peers, pubkey)
		}
	}
	m.saveState()
}

func (m *wgManager) startCleanupLoop() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			m.cleanup()
		}
	}()
}

// wgPubkeyPattern matches a WireGuard Curve25519 public key in base64:
// exactly 43 base64 chars + 1 padding '=' (32 bytes encoded). We do NOT
// decode-then-reject here — `wg` will fail on bad input — but pre-validating
// means we never hand a malformed or attacker-crafted string to a subprocess,
// and we get a clean error message in the log.
//
// Defense-in-depth: req.Pubkey comes from a JSON HTTP body and reaches
// `exec.Command("wg", "set", ..., "peer", pubkey, ...)` as an argv element.
// execve keeps argv slots separate (no shell injection), but we still refuse
// to pass anything that doesn't look like a WireGuard pubkey — protects
// against future regressions and gives better logging.
var wgPubkeyPattern = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)

// validateWgPubkey returns an error if s is not a well-formed WireGuard
// base64 Curve25519 public key (44 chars including padding, decodes to 32 bytes).
func validateWgPubkey(s string) error {
	if !wgPubkeyPattern.MatchString(s) {
		return fmt.Errorf("malformed WireGuard pubkey (want 44-char base64): length=%d", len(s))
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return fmt.Errorf("pubkey base64 decode failed: %w", err)
	}
	if len(decoded) != 32 {
		return fmt.Errorf("pubkey decoded to %d bytes, want 32", len(decoded))
	}
	return nil
}

func wgAddPeer(pubkey, clientIP string) error {
	if err := validateWgPubkey(pubkey); err != nil {
		return fmt.Errorf("wgAddPeer refused bad pubkey: %w", err)
	}
	cmd := exec.Command("wg", "set", wgIfName, "peer", pubkey, "allowed-ips", clientIP+"/32")
	return cmd.Run()
}

func wgRemovePeer(pubkey string) error {
	if err := validateWgPubkey(pubkey); err != nil {
		return fmt.Errorf("wgRemovePeer refused bad pubkey: %w", err)
	}
	cmd := exec.Command("wg", "set", wgIfName, "peer", pubkey, "remove")
	return cmd.Run()
}

func getServerPubkey() string {
	cmd := exec.Command("wg", "show", wgIfName, "public-key")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out[:len(out)-1])
}

type wgConnectRequest struct {
	Token    string `json:"token"`
	Pubkey   string `json:"pubkey"`
	ServerID string `json:"server_id"`
}

type wgConnectResponse struct {
	ClientIP       string `json:"client_ip"`
	SessionTimeout int    `json:"session_timeout"`
	ServerPubkey   string `json:"server_pubkey"`
	DNS            string `json:"dns"`
	ExpiresAt      int64  `json:"expires_at"`
	Endpoint       string `json:"endpoint,omitempty"`
	JWT            string `json:"jwt,omitempty"`
}

func handleWGConnect(mux *http.ServeMux, deps *auth.Dependencies, mgr *wgManager, signer *jwtSigner, registry *serverRegistry) {
	mux.HandleFunc("/v1/wg/connect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req wgConnectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}

		if req.Token == "" || req.Pubkey == "" {
			http.Error(w, `{"error":"token and pubkey required"}`, http.StatusBadRequest)
			return
		}

		h := sha256.Sum256([]byte(req.Pubkey))
		syntheticMAC := fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
			h[0], h[1], h[2], h[3], h[4], h[5])

		result := auth.ProcessAuth(deps, req.Token, syntheticMAC, "", "", "tollgate-wg", "")

		recordAuthLedger(deps, result, "tollgate-wg", "", syntheticMAC)

		if !result.Accept {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			json.NewEncoder(w).Encode(map[string]string{
				"error":   "token rejected",
				"message": result.ReplyMessage,
			})
			return
		}

		expiresAt := time.Now().Unix() + int64(result.SessionTimeout)

		if req.ServerID != "" && signer != nil && registry != nil {
			srv, ok := registry.get(req.ServerID)
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{
					"error": fmt.Sprintf("unknown server_id: %s", req.ServerID),
				})
				return
			}

			parts := strings.SplitN(srv.Subnet, "/", 2)
			base := parts[0]
			octets := strings.Split(base, ".")
			if len(octets) != 4 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid subnet in registry"})
				return
			}
			ipCounter := mgr.nextIP
			if ipCounter < 2 {
				ipCounter = 2
			}
			mgr.nextIP++
			clientIP := fmt.Sprintf("%s.%s.%s.%d", octets[0], octets[1], octets[2], ipCounter)

			jwt, err := registry.signSessionFor(signer, req.ServerID, req.Pubkey, clientIP, expiresAt)
			if err != nil {
				slog.Error("jwt sign failed", "error", err)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": "failed to sign authorization"})
				return
			}

			slog.Info("wg remote session authorized",
				"server_id", req.ServerID,
				"pubkey", truncatePubkey(req.Pubkey),
				"ip", clientIP,
				"expires_at", expiresAt,
			)

			resp := wgConnectResponse{
				ClientIP:       clientIP,
				SessionTimeout: result.SessionTimeout,
				ServerPubkey:   srv.Pubkey,
				DNS:            wgDNS,
				ExpiresAt:      expiresAt,
				Endpoint:       srv.Endpoint,
				JWT:            jwt,
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		tokenHash := ""
		if len(result.LogMessage) > 0 {
			tokenHash = result.LogMessage
		}

		ip, err := mgr.addPeer(req.Pubkey, tokenHash, result.SessionTimeout)
		if err != nil {
			slog.Error("wg add peer", "pubkey", truncatePubkey(req.Pubkey), "error", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{
				"error": fmt.Sprintf("failed to assign WireGuard peer: %v", err),
			})
			return
		}

		slog.Info("wg peer added",
			"action", "added",
			"pubkey", truncatePubkey(req.Pubkey),
			"ip", ip,
			"timeout", result.SessionTimeout,
		)

		resp := wgConnectResponse{
			ClientIP:       ip,
			SessionTimeout: result.SessionTimeout,
			ServerPubkey:   getServerPubkey(),
			DNS:            wgDNS,
			ExpiresAt:      expiresAt,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/v1/wg/disconnect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Pubkey string `json:"pubkey"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}

		if req.Pubkey == "" {
			http.Error(w, `{"error":"pubkey required"}`, http.StatusBadRequest)
			return
		}

		if err := mgr.removePeer(req.Pubkey); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "disconnected"})
	})
}

func truncatePubkey(pubkey string) string {
	if len(pubkey) > 16 {
		return pubkey[:16]
	}
	return pubkey
}
