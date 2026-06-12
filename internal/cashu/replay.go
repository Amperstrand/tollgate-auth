package cashu

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
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
	f, err := os.OpenFile(r.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(thash + "\n")
	f.Sync()
}

// CheckAndMark atomically checks if a hash is spent and marks it if not.
// Returns true if the hash was already spent (caller should reject).
// Uses file locking (flock) for cross-process safety — multiple FreeRADIUS
// worker threads spawn separate Go processes that share the same spent file.
func (r *ReplayGuard) CheckAndMark(thash string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	f, err := os.OpenFile(r.file, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return true // can't open file, assume spent (fail-safe)
	}
	defer f.Close()

	// Exclusive lock for cross-process safety
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return true // can't lock, assume spent
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	data, err := io.ReadAll(f)
	if err != nil {
		return true
	}
	if bytes.Contains(data, []byte(thash)) {
		return true // already spent
	}

	// Seek to end and write
	f.Seek(0, io.SeekEnd)
	f.WriteString(thash + "\n")
	f.Sync()
	return false // not previously spent, now marked
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
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(entry)
	f.WriteString(string(line) + "\n")
}
