package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tollgate-auth/internal/ledger"
)

func openTestLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "settle.jsonl")
	l, err := ledger.OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	return l
}

// seedLedger writes a mix of accepted/rejected auth events and accounting
// events for the given operator. The entries intentionally contain MACs, NAS
// IPs and token hashes so the NoPII test can prove they never reach the report.
func seedLedger(t *testing.T, l *ledger.Ledger, operatorID string) (accepts, rejects, totalSat int) {
	t.Helper()
	now := time.Now().UTC()
	start := now.Add(-24 * time.Hour)
	end := now.Add(time.Hour)

	macs := []string{
		"aa:bb:cc:dd:ee:01",
		"aa:bb:cc:dd:ee:02",
		"aa:bb:cc:dd:ee:03",
	}
	amounts := []int{10, 25, 15}

	for i, mac := range macs {
		entry := ledger.LedgerEntry{
			EventType:   ledger.EventAuthAccept,
			OperatorID:  operatorID,
			MAC:         mac,
			PaymentType: "cashu",
			CreditAmount:   amounts[i],
			DurationSec: amounts[i] * 60,
			MintURL:     "https://testnut.cashu.space",
			TokenHash:   fmt.Sprintf("deadbeef%02d", i),
			NASIP:       fmt.Sprintf("10.0.0.%d", i+1),
			ReplyMessage: fmt.Sprintf("Valid Cashu token: %d sat = %dm access",
				amounts[i], amounts[i]),
		}
		entry.Timestamp = start.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
		if err := l.RecordAuth(entry); err != nil {
			t.Fatalf("RecordAuth accept %d: %v", i, err)
		}
		accepts++
		totalSat += amounts[i]

		// An accounting_stop for each accepted session to ensure those rows
		// exist in the ledger and exercise RevenueSummary's filtering.
		if err := l.RecordAccounting(ledger.LedgerEntry{
			EventType:      ledger.EventAcctStop,
			OperatorID:     operatorID,
			MAC:            mac,
			AcctSessionID:  fmt.Sprintf("sess-%d", i),
			TokenHash:      fmt.Sprintf("deadbeef%02d", i),
			NASIP:          fmt.Sprintf("10.0.0.%d", i+1),
			TerminateCause: "User-Request",
			SessionTime:    int64(amounts[i] * 60),
			InputOctets:    65536,
			OutputOctets:   131072,
			Timestamp:      start.Add(time.Duration(i)*time.Hour + 30*time.Minute).Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("RecordAccounting stop %d: %v", i, err)
		}
	}

	for i := 0; i < 2; i++ {
		if err := l.RecordAuth(ledger.LedgerEntry{
			EventType:    ledger.EventAuthReject,
			OperatorID:   operatorID,
			MAC:          fmt.Sprintf("ff:ff:ff:ff:ff:%02x", i),
			PaymentType:  "cashu",
			TokenHash:    fmt.Sprintf("rejected%02d", i),
			NASIP:        fmt.Sprintf("10.0.0.%d", 10+i),
			ReplyMessage: "Replay detected",
			Timestamp:    end.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("RecordAuth reject %d: %v", i, err)
		}
		rejects++
	}

	return accepts, rejects, totalSat
}

func TestBuildSettlementReport_NoPII(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	const operatorID = "op-no-pii"
	seedLedger(t, l, operatorID)

	now := time.Now().UTC()
	report, err := BuildSettlementReport(l, operatorID, now.Add(-48*time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("BuildSettlementReport: %v", err)
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	jsonStr := string(data)

	// Lowercase comparison: MACs and IPs are case-insensitive in the source.
	lower := strings.ToLower(jsonStr)

	piiSubstrings := []string{
		"aa:bb:cc",    // MAC addresses
		"ff:ff:ff:ff", // reject MAC addresses
		"10.0.0.",     // NAS IP addresses
		"deadbeef",    // token hashes (accepted)
		"rejected00",  // token hashes (rejected, with suffix to avoid matching "rejected_sessions")
		"token_hash",  // field name itself
		"mac",         // field name itself
		"nas_ip",      // field name itself
		"acct_session_id",
		"session_class",
		"terminate_cause",
		"input_octets",
		"output_octets",
		"mint_url",
		"reply_message",
		"cashu", // payment type
		"accounting_stop",
		"auth_accept",
		"auth_reject",
	}
	for _, s := range piiSubstrings {
		if strings.Contains(lower, s) {
			t.Errorf("settlement JSON must not contain %q, but does:\n%s", s, jsonStr)
		}
	}

	// Sanity: the aggregate fields must be present.
	requiredFields := []string{
		`"type":"settlement"`,
		`"operator_id":"op-no-pii"`,
		`"total_sat"`,
		`"accepted_sessions"`,
		`"rejected_sessions"`,
		`"average_credit_amount"`,
		`"period_start"`,
		`"period_end"`,
		`"generated_at"`,
	}
	for _, want := range requiredFields {
		if !strings.Contains(jsonStr, want) {
			t.Errorf("settlement JSON missing %q:\n%s", want, jsonStr)
		}
	}
}

func TestBuildSettlementReport_Aggregation(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	const operatorID = "op-agg"
	accepts, rejects, totalSat := seedLedger(t, l, operatorID)

	now := time.Now().UTC()
	report, err := BuildSettlementReport(l, operatorID, now.Add(-48*time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("BuildSettlementReport: %v", err)
	}

	if report.OperatorID != operatorID {
		t.Errorf("operator_id = %q, want %q", report.OperatorID, operatorID)
	}
	if report.Type != "settlement" {
		t.Errorf("type = %q, want %q", report.Type, "settlement")
	}
	if report.AcceptedSessions != accepts {
		t.Errorf("accepted_sessions = %d, want %d", report.AcceptedSessions, accepts)
	}
	if report.RejectedSessions != rejects {
		t.Errorf("rejected_sessions = %d, want %d", report.RejectedSessions, rejects)
	}
	if report.TotalSat != totalSat {
		t.Errorf("total_sat = %d, want %d", report.TotalSat, totalSat)
	}
	wantAvg := float64(totalSat) / float64(accepts)
	if report.AverageCreditAmount != wantAvg {
		t.Errorf("average_credit_amount = %.4f, want %.4f", report.AverageCreditAmount, wantAvg)
	}
}

func TestBuildSettlementReport_EmptyLedger(t *testing.T) {
	l := openTestLedger(t)
	defer l.Close()

	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)

	report, err := BuildSettlementReport(l, "op-empty", start, end)
	if err != nil {
		t.Fatalf("BuildSettlementReport on empty ledger: %v", err)
	}
	if report.TotalSat != 0 {
		t.Errorf("total_sat = %d, want 0", report.TotalSat)
	}
	if report.AcceptedSessions != 0 {
		t.Errorf("accepted_sessions = %d, want 0", report.AcceptedSessions)
	}
	if report.RejectedSessions != 0 {
		t.Errorf("rejected_sessions = %d, want 0", report.RejectedSessions)
	}
	if report.AverageCreditAmount != 0 {
		t.Errorf("average_credit_amount = %.4f, want 0", report.AverageCreditAmount)
	}
	if report.Type != "settlement" {
		t.Errorf("type = %q, want %q", report.Type, "settlement")
	}
}

func TestSettlementReport_JSONFormat(t *testing.T) {
	report := &SettlementReport{
		Type:             "settlement",
		OperatorID:       "op-json",
		PeriodStart:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:        time.Date(2026, 1, 8, 0, 0, 0, 0, time.UTC),
		TotalSat:         42,
		AcceptedSessions: 3,
		RejectedSessions: 1,
		AverageCreditAmount: 14.0,
		GeneratedAt:      time.Date(2026, 1, 8, 12, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	expectedKeys := map[string]bool{
		"type":               false,
		"operator_id":        false,
		"period_start":       false,
		"period_end":         false,
		"total_sat":          false,
		"accepted_sessions":  false,
		"rejected_sessions":  false,
		"average_credit_amount": false,
		"generated_at":       false,
	}
	for k := range decoded {
		if seen, ok := expectedKeys[k]; ok {
			if seen {
				t.Errorf("key %q appears more than once", k)
			}
			expectedKeys[k] = true
		} else {
			t.Errorf("unexpected key in JSON: %q", k)
		}
	}
	for k, seen := range expectedKeys {
		if !seen {
			t.Errorf("expected key %q missing from JSON", k)
		}
	}

	if decoded["type"] != "settlement" {
		t.Errorf("type = %v, want \"settlement\"", decoded["type"])
	}
	if decoded["operator_id"] != "op-json" {
		t.Errorf("operator_id = %v, want \"op-json\"", decoded["operator_id"])
	}
	// JSON numbers decode as float64.
	if total, ok := decoded["total_sat"].(float64); !ok || total != 42 {
		t.Errorf("total_sat = %v, want 42", decoded["total_sat"])
	}
}
