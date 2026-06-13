package ledger

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTestLedger(t *testing.T) *Ledger {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	l, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	return l
}

func TestRecordAuth_Accept(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	entry := LedgerEntry{
		EventType:    EventAuthAccept,
		OperatorID:   "op-test",
		MAC:          "aa:bb:cc:dd:ee:ff",
		PaymentType:  "cashu",
		AmountSat:    8,
		DurationSec:  480,
		MintURL:      "https://testnut.cashu.space",
		TokenHash:    "abc123",
		ReplyMessage: "Valid Cashu token: 8 sat",
	}
	if err := l.RecordAuth(entry); err != nil {
		t.Fatalf("RecordAuth: %v", err)
	}

	entries, err := l.QueryByMAC("aa:bb:cc:dd:ee:ff", time.Time{})
	if err != nil {
		t.Fatalf("QueryByMAC: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.EventType != EventAuthAccept {
		t.Errorf("expected event_type %q, got %q", EventAuthAccept, e.EventType)
	}
	if e.AmountSat != 8 {
		t.Errorf("expected amount_sat 8, got %d", e.AmountSat)
	}
	if e.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
}

func TestRecordAuth_Reject(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	entry := LedgerEntry{
		EventType:    EventAuthReject,
		OperatorID:   "op-test",
		MAC:          "11:22:33:44:55:66",
		PaymentType:  "cashu",
		ReplyMessage: "Replay detected",
	}
	if err := l.RecordAuth(entry); err != nil {
		t.Fatalf("RecordAuth: %v", err)
	}

	entries, err := l.QueryByMAC("11:22:33:44:55:66", time.Time{})
	if err != nil {
		t.Fatalf("QueryByMAC: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].EventType != EventAuthReject {
		t.Errorf("expected event_type %q, got %q", EventAuthReject, entries[0].EventType)
	}
}

func TestRecordAccounting_Start(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	entry := LedgerEntry{
		EventType:     EventAcctStart,
		MAC:           "aa:bb:cc:dd:ee:ff",
		AcctSessionID: "sess-001",
		NASIP:         "10.0.0.1",
	}
	if err := l.RecordAccounting(entry); err != nil {
		t.Fatalf("RecordAccounting: %v", err)
	}

	entries, err := l.QueryByMAC("aa:bb:cc:dd:ee:ff", time.Time{})
	if err != nil {
		t.Fatalf("QueryByMAC: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].EventType != EventAcctStart {
		t.Errorf("expected %q, got %q", EventAcctStart, entries[0].EventType)
	}
	if entries[0].AcctSessionID != "sess-001" {
		t.Errorf("expected acct_session_id 'sess-001', got %q", entries[0].AcctSessionID)
	}
}

func TestRecordAccounting_Update(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	entry := LedgerEntry{
		EventType:     EventAcctUpdate,
		MAC:           "aa:bb:cc:dd:ee:ff",
		AcctSessionID: "sess-001",
		InputOctets:   1024,
		OutputOctets:  2048,
		SessionTime:   60,
	}
	if err := l.RecordAccounting(entry); err != nil {
		t.Fatalf("RecordAccounting: %v", err)
	}

	entries, err := l.QueryByMAC("aa:bb:cc:dd:ee:ff", time.Time{})
	if err != nil {
		t.Fatalf("QueryByMAC: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].InputOctets != 1024 {
		t.Errorf("expected input_octets 1024, got %d", entries[0].InputOctets)
	}
	if entries[0].OutputOctets != 2048 {
		t.Errorf("expected output_octets 2048, got %d", entries[0].OutputOctets)
	}
	if entries[0].SessionTime != 60 {
		t.Errorf("expected session_time 60, got %d", entries[0].SessionTime)
	}
}

func TestRecordAccounting_Stop(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	entry := LedgerEntry{
		EventType:      EventAcctStop,
		MAC:            "aa:bb:cc:dd:ee:ff",
		AcctSessionID:  "sess-001",
		TerminateCause: "User-Request",
		SessionTime:    480,
		InputOctets:    65536,
		OutputOctets:   131072,
	}
	if err := l.RecordAccounting(entry); err != nil {
		t.Fatalf("RecordAccounting: %v", err)
	}

	entries, err := l.QueryByMAC("aa:bb:cc:dd:ee:ff", time.Time{})
	if err != nil {
		t.Fatalf("QueryByMAC: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].TerminateCause != "User-Request" {
		t.Errorf("expected terminate_cause 'User-Request', got %q", entries[0].TerminateCause)
	}
}

func TestRecordCoA(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	entry := LedgerEntry{
		EventType: EventCoA,
		MAC:       "aa:bb:cc:dd:ee:ff",
		NASIP:     "10.0.0.1",
		Metadata:  `{"reason":"bandwidth_change"}`,
	}
	if err := l.RecordCoA(entry); err != nil {
		t.Fatalf("RecordCoA: %v", err)
	}

	entries, err := l.QueryByMAC("aa:bb:cc:dd:ee:ff", time.Time{})
	if err != nil {
		t.Fatalf("QueryByMAC: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].EventType != EventCoA {
		t.Errorf("expected %q, got %q", EventCoA, entries[0].EventType)
	}
}

func TestQueryByMAC(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	mac1 := "aa:bb:cc:dd:ee:ff"
	mac2 := "11:22:33:44:55:66"

	// Insert 3 entries for mac1.
	for i := 0; i < 3; i++ {
		entry := LedgerEntry{
			EventType: EventAuthAccept,
			MAC:       mac1,
			AmountSat: i + 1,
		}
		if err := l.RecordAuth(entry); err != nil {
			t.Fatalf("RecordAuth %d: %v", i, err)
		}
	}

	// Insert 1 entry for mac2.
	if err := l.RecordAuth(LedgerEntry{EventType: EventAuthAccept, MAC: mac2}); err != nil {
		t.Fatalf("RecordAuth mac2: %v", err)
	}

	entries, err := l.QueryByMAC(mac1, time.Time{})
	if err != nil {
		t.Fatalf("QueryByMAC: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries for mac1, got %d", len(entries))
	}
}

func TestQueryByOperator(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	// Insert entries for operator A.
	for i := 0; i < 2; i++ {
		if err := l.RecordAuth(LedgerEntry{
			EventType:  EventAuthAccept,
			OperatorID: "op-alpha",
			MAC:        fmt.Sprintf("aa:bb:cc:dd:ee:%02x", i),
			AmountSat:  5,
		}); err != nil {
			t.Fatalf("RecordAuth op-alpha: %v", err)
		}
	}

	// Insert entry for operator B.
	if err := l.RecordAuth(LedgerEntry{
		EventType:  EventAuthAccept,
		OperatorID: "op-beta",
		MAC:        "ff:ff:ff:ff:ff:ff",
		AmountSat:  10,
	}); err != nil {
		t.Fatalf("RecordAuth op-beta: %v", err)
	}

	entries, err := l.QueryByOperator("op-alpha", time.Time{})
	if err != nil {
		t.Fatalf("QueryByOperator: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for op-alpha, got %d", len(entries))
	}
	for _, e := range entries {
		if e.OperatorID != "op-alpha" {
			t.Errorf("expected operator_id 'op-alpha', got %q", e.OperatorID)
		}
	}
}

func TestGetActiveSession_Active(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	mac := "aa:bb:cc:dd:ee:ff"
	tokenHash := "deadbeef"

	// Insert auth_accept with a token_hash.
	if err := l.RecordAuth(LedgerEntry{
		EventType:   EventAuthAccept,
		MAC:         mac,
		TokenHash:   tokenHash,
		AmountSat:   8,
		DurationSec: 480,
	}); err != nil {
		t.Fatalf("RecordAuth: %v", err)
	}

	session, err := l.GetActiveSession(mac)
	if err != nil {
		t.Fatalf("GetActiveSession: %v", err)
	}
	if session == nil {
		t.Fatal("expected active session, got nil")
	}
	if session.EventType != EventAuthAccept {
		t.Errorf("expected %q, got %q", EventAuthAccept, session.EventType)
	}
	if session.AmountSat != 8 {
		t.Errorf("expected amount_sat 8, got %d", session.AmountSat)
	}
}

func TestGetActiveSession_Stopped(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	mac := "aa:bb:cc:dd:ee:ff"
	tokenHash := "deadbeef"

	// Insert auth_accept.
	if err := l.RecordAuth(LedgerEntry{
		EventType: EventAuthAccept,
		MAC:       mac,
		TokenHash: tokenHash,
		AmountSat: 8,
	}); err != nil {
		t.Fatalf("RecordAuth: %v", err)
	}

	// Insert accounting_stop with same token_hash.
	if err := l.RecordAccounting(LedgerEntry{
		EventType:      EventAcctStop,
		MAC:            mac,
		TokenHash:      tokenHash,
		TerminateCause: "User-Request",
	}); err != nil {
		t.Fatalf("RecordAccounting: %v", err)
	}

	session, err := l.GetActiveSession(mac)
	if err != nil {
		t.Fatalf("GetActiveSession: %v", err)
	}
	if session != nil {
		t.Errorf("expected nil (stopped session), got entry")
	}
}

func TestGetActiveSession_NoSession(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	session, err := l.GetActiveSession("ff:ff:ff:ff:ff:ff")
	if err != nil {
		t.Fatalf("GetActiveSession: %v", err)
	}
	if session != nil {
		t.Errorf("expected nil (no session), got entry")
	}
}

func TestRevenueSummary(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	operatorID := "op-revenue"
	now := time.Now().UTC()
	start := now.Add(-24 * time.Hour)
	end := now.Add(1 * time.Hour)

	// Insert accepted sessions.
	for i := 0; i < 3; i++ {
		if err := l.RecordAuth(LedgerEntry{
			EventType:  EventAuthAccept,
			OperatorID: operatorID,
			MAC:        fmt.Sprintf("aa:bb:cc:dd:ee:%02x", i),
			AmountSat:  10,
		}); err != nil {
			t.Fatalf("RecordAuth accept: %v", err)
		}
	}

	// Insert rejected sessions.
	for i := 0; i < 2; i++ {
		if err := l.RecordAuth(LedgerEntry{
			EventType:  EventAuthReject,
			OperatorID: operatorID,
			MAC:        fmt.Sprintf("ff:ff:ff:ff:ff:%02x", i),
		}); err != nil {
			t.Fatalf("RecordAuth reject: %v", err)
		}
	}

	report, err := l.RevenueSummary(operatorID, start, end)
	if err != nil {
		t.Fatalf("RevenueSummary: %v", err)
	}
	if report.TotalSessions != 5 {
		t.Errorf("expected total sessions 5, got %d", report.TotalSessions)
	}
	if report.AcceptedSessions != 3 {
		t.Errorf("expected accepted 3, got %d", report.AcceptedSessions)
	}
	if report.RejectedSessions != 2 {
		t.Errorf("expected rejected 2, got %d", report.RejectedSessions)
	}
	if report.TotalSat != 30 {
		t.Errorf("expected total_sat 30, got %d", report.TotalSat)
	}
	expectedAvg := float64(30) / float64(3)
	if report.AverageAmount != expectedAvg {
		t.Errorf("expected average %.2f, got %.2f", expectedAvg, report.AverageAmount)
	}
}

func TestRevenueSummary_EmptyRange(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)

	report, err := l.RevenueSummary("op-none", start, end)
	if err != nil {
		t.Fatalf("RevenueSummary: %v", err)
	}
	if report.TotalSessions != 0 {
		t.Errorf("expected 0 total sessions, got %d", report.TotalSessions)
	}
	if report.AcceptedSessions != 0 {
		t.Errorf("expected 0 accepted, got %d", report.AcceptedSessions)
	}
	if report.TotalSat != 0 {
		t.Errorf("expected 0 total_sat, got %d", report.TotalSat)
	}
	if report.AverageAmount != 0 {
		t.Errorf("expected 0 average, got %.2f", report.AverageAmount)
	}
}

func TestLedgerConcurrentWrites(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	var wg sync.WaitGroup
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			entry := LedgerEntry{
				EventType:  EventAuthAccept,
				OperatorID: "op-concurrent",
				MAC:        fmt.Sprintf("aa:bb:cc:dd:ee:%02x", idx),
				AmountSat:  idx + 1,
			}
			if err := l.RecordAuth(entry); err != nil {
				errors <- fmt.Errorf("goroutine %d: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrent write error: %v", err)
	}

	entries, err := l.QueryByOperator("op-concurrent", time.Time{})
	if err != nil {
		t.Fatalf("QueryByOperator: %v", err)
	}
	if len(entries) != 10 {
		t.Errorf("expected 10 entries, got %d", len(entries))
	}
}

func TestClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "close-test.jsonl")

	l, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}

	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify the ledger file exists.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("ledger file not found after close: %v", err)
	}
}

func TestLedger_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "persist.jsonl")

	// Write an entry.
	l1, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger 1: %v", err)
	}
	entry := LedgerEntry{
		EventType: EventAuthAccept,
		MAC:       "aa:bb:cc:dd:ee:ff",
		AmountSat: 5,
	}
	if err := l1.RecordAuth(entry); err != nil {
		t.Fatalf("RecordAuth: %v", err)
	}
	l1.Close()

	// Reopen and verify the entry persisted.
	l2, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger 2: %v", err)
	}
	defer l2.Close()

	entries, err := l2.QueryByMAC("aa:bb:cc:dd:ee:ff", time.Time{})
	if err != nil {
		t.Fatalf("QueryByMAC: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after reopen, got %d", len(entries))
	}
	if entries[0].AmountSat != 5 {
		t.Errorf("expected amount 5, got %d", entries[0].AmountSat)
	}
}
