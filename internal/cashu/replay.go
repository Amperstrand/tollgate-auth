package cashu

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// ReplayGuard tracks spent token hashes to prevent double-spending.
type ReplayGuard struct {
	file string
	mu   sync.Mutex
}

// NewReplayGuard creates a ReplayGuard backed by the given file path.
func NewReplayGuard(file string) *ReplayGuard {
	return &ReplayGuard{file: file}
}

// IsSpent checks if a token hash has been recorded as spent.
func (r *ReplayGuard) IsSpent(thash string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := os.ReadFile(r.file)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), thash)
}

// MarkSpent records a token hash as spent.
func (r *ReplayGuard) MarkSpent(thash string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := os.OpenFile(r.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(thash + "\n")
	f.Sync()
}

// LogToken appends a token attempt to the JSONL log file.
func LogToken(tokenStr string, tokenData *TokenData, guest string, accepted bool, logFile string) {
	LogTokenWithError(tokenStr, tokenData, guest, accepted, "", logFile)
}

// LogTokenWithError appends a token attempt with optional error details to the JSONL log file.
func LogTokenWithError(tokenStr string, tokenData *TokenData, guest string, accepted bool, errMsg string, logFile string) {
	entry := map[string]interface{}{
		"ts":       time.Now().UTC().Format(time.RFC3339),
		"guest":    guest,
		"accepted": accepted,
		"mint":     tokenData.Mint,
		"amount":   tokenData.Amount,
		"unit":     tokenData.Unit,
		"hash":     TokenHash(tokenStr),
	}
	if errMsg != "" {
		entry["error"] = errMsg
	}
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(entry)
	f.WriteString(string(line) + "\n")
}
