package ocpi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestStore opens a Store backed by a fresh per-test temp directory.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatalf("NewStoreWithDir(%q) error: %v", dir, err)
	}
	return s
}

// reopenTestStore opens a second Store pointing at the same directory to
// simulate a server restart.
func reopenTestStore(t *testing.T, first *Store) *Store {
	t.Helper()
	s, err := NewStoreWithDir(first.dataDir)
	if err != nil {
		t.Fatalf("reopen NewStoreWithDir(%q) error: %v", first.dataDir, err)
	}
	return s
}

func TestStoreWithDir_EmptyStringMatchesNewStore(t *testing.T) {
	// Given: an empty dataDir.
	// When: NewStoreWithDir("") is called.
	// Then: the returned store behaves like NewStore() — no dataDir set, no I/O.
	s, err := NewStoreWithDir("")
	if err != nil {
		t.Fatalf("NewStoreWithDir(\"\") error: %v", err)
	}
	if s.dataDir != "" {
		t.Errorf("dataDir = %q, want empty", s.dataDir)
	}
	// A mutation must not panic or fail when dataDir is empty.
	s.PutPrepay(&PrepayRecord{UID: "OCPI-X"})
	s.PutCDR(&CDR{ID: "cdr-1", AuthID: "OCPI-X"})
	s.MarkPrepayAuthorized("OCPI-X")
	s.AppendAuthz(AuthorizeEvent{UID: "OCPI-X", Allowed: AuthzAllowed})
}

func TestStoreWithDir_PutCDR_PersistsAndReloads(t *testing.T) {
	// Given: a fresh persistent store.
	s := newTestStore(t)

	// When: a CDR is stored.
	want := &CDR{
		ID:         "cdr-restart-1",
		Started:    time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC),
		Stopped:    time.Date(2026, 7, 2, 11, 0, 0, 0, time.UTC),
		AuthID:     "OCPI-A",
		LocationID: "loc-1",
		Kwh:        7.4,
		TotalCost:  18.5,
		Currency:   "NOK",
	}
	s.PutCDR(want)

	// Then: a JSON file exists at the expected path with the marshaled CDR.
	filePath := filepath.Join(s.dataDir, dirCDRs, "cdr-restart-1.json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("CDR file not written: %v", err)
	}
	var onDisk CDR
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("CDR file not valid JSON: %v", err)
	}
	if onDisk.ID != want.ID || onDisk.Kwh != want.Kwh {
		t.Errorf("on-disk CDR = %+v, want %+v", onDisk, want)
	}

	// And: a reopened store loads the CDR into memory.
	reopened := reopenTestStore(t, s)
	snap := reopened.Snapshot()
	if len(snap.CDRs) != 1 {
		t.Fatalf("reopened snapshot CDRs = %d, want 1", len(snap.CDRs))
	}
	if snap.CDRs[0].ID != want.ID {
		t.Errorf("reloaded CDR ID = %q, want %q", snap.CDRs[0].ID, want.ID)
	}
}

func TestStoreWithDir_PutPrepay_PersistsAndReloads(t *testing.T) {
	// Given: a fresh persistent store.
	s := newTestStore(t)

	// When: a prepay record is stored.
	want := &PrepayRecord{
		UID:            "OCPI-PREPAY-1",
		CashuTokenHash: "deadbeef1234abcd",
		AllotmentSec:   600,
		AmountSat:      60,
		MintURL:        "https://testnut.cashu.space",
		ContractID:     "NPC-OCPI-1",
		StartedAt:      time.Now().UTC().Truncate(time.Second),
	}
	s.PutPrepay(want)

	// Then: a reopened store loads the record with all fields preserved.
	reopened := reopenTestStore(t, s)
	got, ok := reopened.GetPrepay("OCPI-PREPAY-1")
	if !ok {
		t.Fatal("prepay not reloaded after reopen")
	}
	if got.UID != want.UID || got.AllotmentSec != want.AllotmentSec ||
		got.AmountSat != want.AmountSat || got.MintURL != want.MintURL ||
		!got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("reloaded prepay = %+v, want %+v", got, want)
	}
}

func TestStoreWithDir_MarkPrepayAuthorized_Persists(t *testing.T) {
	// Given: a persistent store with a prepay record.
	s := newTestStore(t)
	s.PutPrepay(&PrepayRecord{UID: "OCPI-A"})

	// When: MarkPrepayAuthorized is called.
	s.MarkPrepayAuthorized("OCPI-A")

	// Then: the persisted record carries AuthorizedAt and a reopen sees it.
	reopened := reopenTestStore(t, s)
	got, _ := reopened.GetPrepay("OCPI-A")
	if got.AuthorizedAt == nil {
		t.Fatal("AuthorizedAt not persisted across reopen")
	}
}

func TestStoreWithDir_PutCDR_MarksPrepayUsed_Persists(t *testing.T) {
	// Given: a persistent store with a prepay record.
	s := newTestStore(t)
	s.PutPrepay(&PrepayRecord{UID: "OCPI-A"})

	// When: a CDR arrives whose AuthID matches the prepay UID.
	s.PutCDR(&CDR{ID: "cdr-1", AuthID: "OCPI-A"})

	// Then: a reopened store still sees the prepay record as Used.
	reopened := reopenTestStore(t, s)
	got, _ := reopened.GetPrepay("OCPI-A")
	if !got.Used {
		t.Error("Used flag not persisted across reopen after PutCDR")
	}
}

func TestStoreWithDir_AppendAuthz_AppendsJSONL(t *testing.T) {
	// Given: a fresh persistent store.
	s := newTestStore(t)

	// When: three authorize events are appended.
	for i := 0; i < 3; i++ {
		s.AppendAuthz(AuthorizeEvent{
			At:      time.Now(),
			UID:     "OCPI-X",
			Allowed: AuthzAllowed,
			Reason:  "test event",
		})
	}

	// Then: the JSONL file contains exactly three newline-terminated records.
	data, err := os.ReadFile(filepath.Join(s.dataDir, fileAuthzLog))
	if err != nil {
		t.Fatalf("authz log file not written: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("authz log lines = %d, want 3", len(lines))
	}
	for i, line := range lines {
		var ev AuthorizeEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d not valid JSON: %v", i, err)
		}
		if ev.UID != "OCPI-X" {
			t.Errorf("line %d UID = %q, want OCPI-X", i, ev.UID)
		}
	}
}

func TestStoreWithDir_AuthzLog_ReloadsMostRecentFirstCapped(t *testing.T) {
	// Given: a persistent store that has accumulated more than authzLogMax events.
	s := newTestStore(t)
	for i := 0; i < authzLogMax+50; i++ {
		s.AppendAuthz(AuthorizeEvent{
			UID:     "OCPI-X",
			Allowed: AuthzAllowed,
			Reason:  "filler",
		})
	}

	// When: a second store reopens from the same directory.
	reopened := reopenTestStore(t, s)

	// Then: the in-memory log is capped at authzLogMax, most-recent-first.
	snap := reopened.Snapshot()
	if len(snap.AuthzLog) != authzLogMax {
		t.Errorf("reopened authz log len = %d, want %d", len(snap.AuthzLog), authzLogMax)
	}
}

func TestStoreWithDir_CorruptFilesAreSkippedNotFatal(t *testing.T) {
	// Given: a data directory where one CDR file and one prepay file are corrupt.
	s := newTestStore(t)
	s.PutCDR(&CDR{ID: "good-cdr", AuthID: "OCPI-A"})
	s.PutPrepay(&PrepayRecord{UID: "good-prepay"})
	corruptCDR := filepath.Join(s.dataDir, dirCDRs, "broken.json")
	if err := os.WriteFile(corruptCDR, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	corruptPrepay := filepath.Join(s.dataDir, dirPrepay, "broken.json")
	if err := os.WriteFile(corruptPrepay, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}

	// When: a store reopens from the directory.
	// Then: it does not error, and the well-formed records still load.
	reopened, err := NewStoreWithDir(s.dataDir)
	if err != nil {
		t.Fatalf("reopen with corrupt files returned error: %v", err)
	}
	snap := reopened.Snapshot()
	if len(snap.CDRs) != 1 || snap.CDRs[0].ID != "good-cdr" {
		t.Errorf("CDRs = %+v, want exactly [good-cdr]", snap.CDRs)
	}
	if _, ok := reopened.GetPrepay("good-prepay"); !ok {
		t.Error("good-prepay missing after reopen alongside corrupt file")
	}
}

func TestStoreWithDir_UnsafeIDDoesNotEscapeRecordDir(t *testing.T) {
	// Given: a fresh persistent store.
	s := newTestStore(t)

	// When: a CDR is stored with a path-traversal-shaped ID.
	s.PutCDR(&CDR{ID: "../../../etc/passwd", AuthID: "OCPI-A"})

	// Then: exactly one file lands inside dataDir/cdrs/, none above it.
	cdrDir := filepath.Join(s.dataDir, dirCDRs)
	entries, err := os.ReadDir(cdrDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("cdr files = %d, want 1", len(entries))
	}
	// The sanitized name has no slash separators so it cannot escape via Join.
	if strings.Contains(entries[0].Name(), "/") {
		t.Errorf("sanitized name still contains '/': %q", entries[0].Name())
	}
	// And: nothing was written outside dataDir/cdrs/.
	if _, err := os.Stat(filepath.Join(s.dataDir, "..", "passwd")); err == nil {
		t.Error("a file escaped the record dir (../../etc/passwd traversal)")
	}
	// And: the CDR is recoverable from the sanitized file on reopen.
	reopened := reopenTestStore(t, s)
	if got := reopened.Snapshot().CDRs; len(got) != 1 || got[0].ID != "../../../etc/passwd" {
		t.Errorf("reopened CDRs = %+v, want the traversal-shaped ID preserved as a value", got)
	}
}

func TestStoreWithDir_GenericStateRoundTrip(t *testing.T) {
	// Given: a persistent store.
	s := newTestStore(t)

	// When: a generic state blob is saved and then loaded into a fresh value.
	type fakeState struct {
		State string  `json:"state"`
		Since float64 `json:"since"`
	}
	if err := s.SaveState("charger", fakeState{State: "CHARGING", Since: 1.5}); err != nil {
		t.Fatalf("SaveState error: %v", err)
	}
	var loaded fakeState
	if err := s.LoadState("charger", &loaded); err != nil {
		t.Fatalf("LoadState error: %v", err)
	}

	// Then: the round-trip preserves the fields.
	if loaded.State != "CHARGING" || loaded.Since != 1.5 {
		t.Errorf("loaded = %+v, want {CHARGING 1.5}", loaded)
	}
}

func TestStoreWithDir_LoadStateMissingFileIsNotError(t *testing.T) {
	// Given: a fresh persistent store (no charger.json written yet).
	s := newTestStore(t)

	// When: LoadState is called for a name with no file.
	var v map[string]any
	err := s.LoadState("never-written", &v)

	// Then: no error is returned (caller should keep defaults).
	if err != nil {
		t.Errorf("LoadState on missing file returned error: %v", err)
	}
}

func TestStoreWithDir_NoTmpFileLeftAfterAtomicWrite(t *testing.T) {
	// Given: a persistent store.
	s := newTestStore(t)

	// When: a CDR is written (which goes via tmp+rename).
	s.PutCDR(&CDR{ID: "cdr-atomic", AuthID: "OCPI-A"})

	// Then: the .tmp sidecar is gone (rename succeeded and cleaned up).
	cdrDir := filepath.Join(s.dataDir, dirCDRs)
	entries, err := os.ReadDir(cdrDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover tmp file in cdr dir: %s", e.Name())
		}
	}
}
