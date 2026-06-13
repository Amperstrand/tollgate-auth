package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Ledger is a JSONL append-only log file backed ledger.
type Ledger struct {
	path string
	mu   sync.Mutex
}

// OpenLedger opens (or creates) the JSONL ledger at the given path.
// Creates the parent directory and file if they don't exist.
func OpenLedger(path string) (*Ledger, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("ledger: create directory: %w", err)
	}

	// Create/touch the file if it doesn't exist.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("ledger: create file: %w", err)
	}
	f.Close()

	return &Ledger{path: path}, nil
}

// Close is a no-op for JSONL (no persistent connection).
func (l *Ledger) Close() error {
	return nil
}

// appendEntry marshals the entry to JSON and appends it as a new line.
func (l *Ledger) appendEntry(entry LedgerEntry) error {
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("ledger: marshal entry: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("ledger: open file for append: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("ledger: write entry: %w", err)
	}
	if _, err := f.Write([]byte("\n")); err != nil {
		return fmt.Errorf("ledger: write newline: %w", err)
	}

	return nil
}

// RecordAuth records an authentication event (accept or reject).
func (l *Ledger) RecordAuth(entry LedgerEntry) error {
	return l.appendEntry(entry)
}

// RecordAccounting records an accounting event (start/update/stop).
func (l *Ledger) RecordAccounting(entry LedgerEntry) error {
	return l.appendEntry(entry)
}

// RecordCoA records a CoA or disconnect event.
func (l *Ledger) RecordCoA(entry LedgerEntry) error {
	return l.appendEntry(entry)
}

// readAll reads every entry from the JSONL file.
func (l *Ledger) readAll() ([]LedgerEntry, error) {
	f, err := os.Open(l.path)
	if err != nil {
		return nil, fmt.Errorf("ledger: open file for read: %w", err)
	}
	defer f.Close()

	var entries []LedgerEntry

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e LedgerEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("ledger: unmarshal entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ledger: scan file: %w", err)
	}

	return entries, nil
}

// QueryByMAC returns all ledger entries for a MAC address since the given time,
// sorted by timestamp descending (most recent first).
func (l *Ledger) QueryByMAC(mac string, since time.Time) ([]LedgerEntry, error) {
	entries, err := l.readAll()
	if err != nil {
		return nil, fmt.Errorf("ledger: query by MAC: %w", err)
	}

	var filtered []LedgerEntry
	for _, e := range entries {
		if e.MAC != mac {
			continue
		}
		if !since.IsZero() && parseTime(e.Timestamp).Before(since) {
			continue
		}
		filtered = append(filtered, e)
	}

	// Reverse for most-recent-first (entries are appended in time order).
	return reverseEntries(filtered), nil
}

// QueryByOperator returns all ledger entries for an operator since the given time,
// sorted by timestamp descending (most recent first).
func (l *Ledger) QueryByOperator(operatorID string, since time.Time) ([]LedgerEntry, error) {
	entries, err := l.readAll()
	if err != nil {
		return nil, fmt.Errorf("ledger: query by operator: %w", err)
	}

	var filtered []LedgerEntry
	for _, e := range entries {
		if e.OperatorID != operatorID {
			continue
		}
		if !since.IsZero() && parseTime(e.Timestamp).Before(since) {
			continue
		}
		filtered = append(filtered, e)
	}

	// Reverse for most-recent-first (entries are appended in time order).
	return reverseEntries(filtered), nil
}

// GetActiveSession returns the most recent auth_accept for a MAC that hasn't been stopped.
// Used for session reconnection checks.
func (l *Ledger) GetActiveSession(mac string) (*LedgerEntry, error) {
	entries, err := l.readAll()
	if err != nil {
		return nil, fmt.Errorf("ledger: get active session: %w", err)
	}

	// Find the most recent auth_accept for this MAC (iterate in reverse).
	var accept *LedgerEntry
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.MAC == mac && e.EventType == EventAuthAccept {
			accept = &e
			break
		}
	}

	if accept == nil {
		return nil, nil
	}

	// Check for a corresponding accounting_stop with the same token_hash.
	if accept.TokenHash != "" {
		for _, e := range entries {
			if e.MAC == mac &&
				e.EventType == EventAcctStop &&
				e.TokenHash == accept.TokenHash {
				return nil, nil // session has been stopped
			}
		}
	}

	return accept, nil
}

// RevenueSummary returns a revenue report for an operator over a time range.
func (l *Ledger) RevenueSummary(operatorID string, start, end time.Time) (*RevenueReport, error) {
	entries, err := l.readAll()
	if err != nil {
		return nil, fmt.Errorf("ledger: revenue summary: %w", err)
	}

	report := &RevenueReport{
		OperatorID: operatorID,
		StartTime:  start,
		EndTime:    end,
	}

	for _, e := range entries {
		if e.OperatorID != operatorID {
			continue
		}
		t := parseTime(e.Timestamp)
		if t.Before(start) || t.After(end) {
			continue
		}

		switch e.EventType {
		case EventAuthAccept:
			report.TotalSessions++
			report.AcceptedSessions++
			report.TotalSat += e.AmountSat
		case EventAuthReject:
			report.TotalSessions++
			report.RejectedSessions++
		}
	}

	if report.AcceptedSessions > 0 {
		report.AverageAmount = float64(report.TotalSat) / float64(report.AcceptedSessions)
	}

	return report, nil
}

// parseTime parses an RFC3339 timestamp string, returning zero time on error.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// reverseEntries returns a new slice with entries in reverse order.
func reverseEntries(entries []LedgerEntry) []LedgerEntry {
	n := len(entries)
	reversed := make([]LedgerEntry, n)
	for i := 0; i < n; i++ {
		reversed[i] = entries[n-1-i]
	}
	return reversed
}
