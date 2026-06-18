package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
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
			log.Printf("WG: warning: failed to remove old peer: %v", err)
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
		log.Printf("WG: warning: wg remove peer: %v", err)
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
				log.Printf("WG: cleanup: wg remove %s: %v", pubkey[:16], err)
			} else {
				log.Printf("WG: cleanup: removed expired peer (pubkey=%s..., ip=%s)", pubkey[:16], peer.IP)
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

func wgAddPeer(pubkey, clientIP string) error {
	cmd := exec.Command("wg", "set", wgIfName, "peer", pubkey, "allowed-ips", clientIP+"/32")
	return cmd.Run()
}

func wgRemovePeer(pubkey string) error {
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
	Token  string `json:"token"`
	Pubkey string `json:"pubkey"`
}

type wgConnectResponse struct {
	ClientIP       string `json:"client_ip"`
	SessionTimeout int    `json:"session_timeout"`
	ServerPubkey   string `json:"server_pubkey"`
	DNS            string `json:"dns"`
	ExpiresAt      int64  `json:"expires_at"`
}

func handleWGConnect(mux *http.ServeMux, deps *auth.Dependencies, mgr *wgManager) {
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

		result := auth.ProcessAuth(deps, req.Token, syntheticMAC, "", "")

		if !result.Accept {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			json.NewEncoder(w).Encode(map[string]string{
				"error":   "token rejected",
				"message": result.ReplyMessage,
			})
			return
		}

		tokenHash := ""
		if len(result.LogMessage) > 0 {
			tokenHash = result.LogMessage
		}

		ip, err := mgr.addPeer(req.Pubkey, tokenHash, result.SessionTimeout)
		if err != nil {
			log.Printf("WG: failed to add peer: %v", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{
				"error": fmt.Sprintf("failed to assign WireGuard peer: %v", err),
			})
			return
		}

		log.Printf("WG: peer added pubkey=%s... ip=%s timeout=%ds",
			req.Pubkey[:min(16, len(req.Pubkey))], ip, result.SessionTimeout)

		resp := wgConnectResponse{
			ClientIP:       ip,
			SessionTimeout: result.SessionTimeout,
			ServerPubkey:   getServerPubkey(),
			DNS:            wgDNS,
			ExpiresAt:      time.Now().Unix() + int64(result.SessionTimeout),
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
