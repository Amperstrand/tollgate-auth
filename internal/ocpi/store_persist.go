package ocpi

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// NewStoreWithDir returns a store backed by dataDir. Existing on-disk state is
// loaded into memory before the store is returned. The directory tree
// (dataDir, dataDir/cdrs, dataDir/prepay) is created with 0700 if missing.
// Pass an empty string to get the same behavior as NewStore (in-memory only).
func NewStoreWithDir(dataDir string) (*Store, error) {
	if dataDir == "" {
		return NewStore(), nil
	}
	for _, sub := range []string{"", dirCDRs, dirPrepay} {
		if err := os.MkdirAll(filepath.Join(dataDir, sub), 0700); err != nil {
			return nil, err
		}
	}
	s := &Store{
		dataDir:  dataDir,
		tokens:   make(map[string]*PrepayRecord),
		sessions: make(map[string]*Session),
		cdrs:     make(map[string]*CDR),
	}
	if err := s.loadFromDisk(); err != nil {
		return nil, err
	}
	return s, nil
}

// persistCDRLocked writes one CDR file atomically. Caller holds s.mu.
// No-op when dataDir is empty or the record has no ID.
func (s *Store) persistCDRLocked(cdr *CDR) {
	if s.dataDir == "" || cdr == nil || cdr.ID == "" {
		return
	}
	path := filepath.Join(s.dataDir, dirCDRs, sanitizeFileName(cdr.ID)+".json")
	if err := writeJSONFileAtomic(path, cdr); err != nil {
		slog.Warn("ocpi persist cdr failed", "id", cdr.ID, "error", err)
	}
}

// persistPrepayLocked writes one prepay record file atomically. Caller holds s.mu.
// No-op when dataDir is empty or the record has no UID.
func (s *Store) persistPrepayLocked(r *PrepayRecord) {
	if s.dataDir == "" || r == nil || r.UID == "" {
		return
	}
	path := filepath.Join(s.dataDir, dirPrepay, sanitizeFileName(r.UID)+".json")
	if err := writeJSONFileAtomic(path, r); err != nil {
		slog.Warn("ocpi persist prepay failed", "uid", r.UID, "error", err)
	}
}

// persistAuthzLocked appends one event as a single JSON line to authz.jsonl.
// Caller holds s.mu. Small append writes (< PIPE_BUF) are atomic on POSIX.
// No-op when dataDir is empty.
func (s *Store) persistAuthzLocked(ev AuthorizeEvent) {
	if s.dataDir == "" {
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		slog.Warn("ocpi persist authz marshal failed", "error", err)
		return
	}
	data = append(data, '\n')
	path := filepath.Join(s.dataDir, fileAuthzLog)
	// O_APPEND on POSIX guarantees the write is atomic with respect to other
	// appenders; we additionally hold s.mu so concurrent appends serialize.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		slog.Warn("ocpi persist authz open failed", "error", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		slog.Warn("ocpi persist authz write failed", "error", err)
	}
}

// loadFromDisk repopulates in-memory state from dataDir. Called once at
// construction by NewStoreWithDir. Missing files or directories are not errors;
// they simply yield an empty store. Corrupt entries are skipped with a warning.
func (s *Store) loadFromDisk() error {
	if s.dataDir == "" {
		return nil
	}
	s.loadCDRsFromDisk()
	s.loadPrepayFromDisk()
	s.loadAuthzFromDisk()
	return nil
}

func (s *Store) loadCDRsFromDisk() {
	cdrDir := filepath.Join(s.dataDir, dirCDRs)
	entries, err := os.ReadDir(cdrDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cdrDir, e.Name()))
		if err != nil {
			continue
		}
		var c CDR
		if err := json.Unmarshal(data, &c); err == nil && c.ID != "" {
			s.cdrs[c.ID] = &c
		} else if err != nil {
			slog.Warn("ocpi load skipping corrupt cdr file", "file", e.Name(), "error", err)
		}
	}
}

func (s *Store) loadPrepayFromDisk() {
	prepayDir := filepath.Join(s.dataDir, dirPrepay)
	entries, err := os.ReadDir(prepayDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(prepayDir, e.Name()))
		if err != nil {
			continue
		}
		var r PrepayRecord
		if err := json.Unmarshal(data, &r); err == nil && r.UID != "" {
			s.tokens[r.UID] = &r
		} else if err != nil {
			slog.Warn("ocpi load skipping corrupt prepay file", "file", e.Name(), "error", err)
		}
	}
}

// loadAuthzFromDisk reads authz.jsonl, parses each line, and stores the most
// recent authzLogMax events most-recent-first to match the in-memory invariant.
func (s *Store) loadAuthzFromDisk() {
	path := filepath.Join(s.dataDir, fileAuthzLog)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var events []AuthorizeEvent
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev AuthorizeEvent
		if err := json.Unmarshal([]byte(line), &ev); err == nil {
			events = append(events, ev)
		}
	}
	if len(events) > authzLogMax {
		events = events[len(events)-authzLogMax:]
	}
	s.authzLog = make([]*AuthorizeEvent, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		s.authzLog = append(s.authzLog, &ev)
	}
}

// LoadState reads a generic named state file into v. Used by callers outside
// the store that own their own state (e.g. Server for charger state). Returns
// nil (not an error) when no state file exists yet — the caller keeps defaults.
func (s *Store) LoadState(name string, v any) error {
	if s.dataDir == "" {
		return nil
	}
	path := filepath.Join(s.dataDir, sanitizeFileName(name)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, v)
}

// SaveState writes a generic named state file atomically. Used by callers
// outside the store that own their own state. No-op when dataDir is empty.
func (s *Store) SaveState(name string, v any) error {
	if s.dataDir == "" {
		return nil
	}
	path := filepath.Join(s.dataDir, sanitizeFileName(name)+".json")
	return writeJSONFileAtomic(path, v)
}

// writeJSONFileAtomic marshals v and writes it to path via tmp+rename so a
// crash mid-write cannot corrupt the destination. The tmp file lives on the
// same filesystem as path (same directory), so rename is atomic on POSIX.
func writeJSONFileAtomic(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sanitizeFileName keeps alphanumerics, '-', '_', and '.'; replaces everything
// else (path separators, control chars, spaces) with '_' so untrusted CDR IDs
// and token UIDs cannot escape the on-disk record directory.
func sanitizeFileName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
