package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tollgate-auth/internal/cashu"
)

// --- Configuration ---

const (
	BaseDir       = "/opt/cashu-tollgate"
	WalletDir     = "/var/lib/cashu-wallet"
	TokensLogFile = BaseDir + "/radius-tokens.log"
	RateSecPerSat = 60
)

// SessionStore tracks active RADIUS sessions by MAC address.
// When a phone reconnects (sleep/wake), we skip token validation
// if the session is still within its paid time window.
type SessionStore struct {
	Dir string
}

type SessionRecord struct {
	MAC      string    `json:"mac"`
	Token    string    `json:"token_hash"`
	Guest    string    `json:"guest"`
	Mint     string    `json:"mint"`
	Amount   int       `json:"amount"`
	Started  time.Time `json:"started"`
	Duration int       `json:"duration"` // seconds
}

func (s *SessionStore) Path(mac string) string {
	// Normalize MAC: lowercase, strip colons/dashes
	mac = strings.ToLower(mac)
	mac = strings.ReplaceAll(mac, ":", "")
	mac = strings.ReplaceAll(mac, "-", "")
	return filepath.Join(s.Dir, mac+".json")
}

func (s *SessionStore) Get(mac string) (*SessionRecord, bool) {
	data, err := os.ReadFile(s.Path(mac))
	if err != nil {
		return nil, false
	}
	var rec SessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, false
	}
	return &rec, true
}

func (s *SessionStore) IsActive(rec *SessionRecord) bool {
	deadline := rec.Started.Add(time.Duration(rec.Duration) * time.Second)
	return time.Now().Before(deadline)
}

func (s *SessionStore) Save(rec *SessionRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path(rec.MAC), data, 0600)
}

func (s *SessionStore) Remove(mac string) {
	os.Remove(s.Path(mac))
}

// --- Main ---

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <username> <mac-address>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  Called by FreeRADIUS exec module.\n")
		fmt.Fprintf(os.Stderr, "  username = Cashu token (inner User-Name from PEAP tunnel)\n")
		fmt.Fprintf(os.Stderr, "  mac-address = Calling-Station-Id (e.g. aa:bb:cc:dd:ee:ff)\n")
		os.Exit(1)
	}

	username := os.Args[1]
	mac := os.Args[2]

	sessions := &SessionStore{Dir: BaseDir + "/radius-sessions"}
	os.MkdirAll(sessions.Dir, 0700)

	replay := cashu.NewReplayGuard(BaseDir + "/radius-spent.txt")
	os.MkdirAll(BaseDir, 0755)

	// --- Check for existing active session (reconnection) ---
	if rec, found := sessions.Get(mac); found {
		if sessions.IsActive(rec) {
			remaining := time.Until(rec.Started.Add(time.Duration(rec.Duration) * time.Second))
			log.Printf("Reconnection: MAC=%s session active (%dm remaining), accepting", mac, int(remaining.Minutes()))
			os.Exit(0)
		}
		// Session expired, clean up
		log.Printf("Reconnection: MAC=%s session expired, removing", mac)
		sessions.Remove(mac)
	}

	// --- Validate Cashu token ---
	tokenData, err := cashu.DecodeToken(username)
	if err != nil {
		log.Printf("Reject: token decode failed: %v", err)
		cashu.LogToken(username, &cashu.TokenData{}, "radius-"+mac, false, TokensLogFile)
		os.Exit(1)
	}

	thash := cashu.TokenHash(username)
	seconds := tokenData.Amount * RateSecPerSat

	// Replay check
	if replay.IsSpent(thash) {
		log.Printf("Reject: token already spent (hash=%s)", thash[:16])
		os.Exit(1)
	}

	// Mint verification
	ok, msg := cashu.VerifyWithMint(tokenData)
	if !ok {
		log.Printf("Reject: mint verification failed: %s", msg)
		cashu.LogToken(username, tokenData, "radius-"+mac, false, TokensLogFile)
		os.Exit(1)
	}

	// Redeem token
	if err := cashu.RedeemToken(username, WalletDir); err != nil {
		log.Printf("Reject: token redemption failed: %v", err)
		cashu.LogToken(username, tokenData, "radius-"+mac, false, TokensLogFile)
		os.Exit(1)
	}

	// Mark spent & log
	replay.MarkSpent(thash)
	cashu.LogToken(username, tokenData, "radius-"+mac, true, TokensLogFile)

	// Save session for reconnection
	rec := &SessionRecord{
		MAC:      mac,
		Token:    thash,
		Guest:    "radius-" + thash[:8],
		Mint:     tokenData.Mint,
		Amount:   tokenData.Amount,
		Started:  time.Now(),
		Duration: seconds,
	}
	if err := sessions.Save(rec); err != nil {
		log.Printf("Warning: failed to save session record: %v", err)
	}

	log.Printf("Accept: MAC=%s amount=%d sat duration=%ds mint=%s",
		mac, tokenData.Amount, seconds, tokenData.Mint)
	os.Exit(0)
}
