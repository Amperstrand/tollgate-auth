package ledger

import (
	"testing"
)

func TestParseAccountingEvent_Start(t *testing.T) {
	entry, err := ParseAccountingEvent(
		"Start", "sess-001", "aa:bb:cc:dd:ee:ff", "user1",
		"", "", "", "", "10.0.0.1",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.EventType != EventAcctStart {
		t.Errorf("expected %q, got %q", EventAcctStart, entry.EventType)
	}
	if entry.AcctSessionID != "sess-001" {
		t.Errorf("expected session ID 'sess-001', got %q", entry.AcctSessionID)
	}
	if entry.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("expected MAC 'aa:bb:cc:dd:ee:ff', got %q", entry.MAC)
	}
	if entry.NASIP != "10.0.0.1" {
		t.Errorf("expected NAS IP '10.0.0.1', got %q", entry.NASIP)
	}
}

func TestParseAccountingEvent_InterimUpdateWithData(t *testing.T) {
	entry, err := ParseAccountingEvent(
		"Interim-Update", "sess-002", "11:22:33:44:55:66", "user2",
		"60", "1024", "2048", "", "10.0.0.1",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.EventType != EventAcctUpdate {
		t.Errorf("expected %q, got %q", EventAcctUpdate, entry.EventType)
	}
	if entry.SessionTime != 60 {
		t.Errorf("expected session_time 60, got %d", entry.SessionTime)
	}
	if entry.InputOctets != 1024 {
		t.Errorf("expected input_octets 1024, got %d", entry.InputOctets)
	}
	if entry.OutputOctets != 2048 {
		t.Errorf("expected output_octets 2048, got %d", entry.OutputOctets)
	}
}

func TestParseAccountingEvent_StopWithAllFields(t *testing.T) {
	entry, err := ParseAccountingEvent(
		"Stop", "sess-003", "aa:bb:cc:dd:ee:ff", "user1",
		"480", "65536", "131072", "User-Request", "10.0.0.1",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.EventType != EventAcctStop {
		t.Errorf("expected %q, got %q", EventAcctStop, entry.EventType)
	}
	if entry.SessionTime != 480 {
		t.Errorf("expected session_time 480, got %d", entry.SessionTime)
	}
	if entry.InputOctets != 65536 {
		t.Errorf("expected input_octets 65536, got %d", entry.InputOctets)
	}
	if entry.OutputOctets != 131072 {
		t.Errorf("expected output_octets 131072, got %d", entry.OutputOctets)
	}
	if entry.TerminateCause != "User-Request" {
		t.Errorf("expected terminate_cause 'User-Request', got %q", entry.TerminateCause)
	}
}

func TestParseAccountingEvent_NumericStatusTypes(t *testing.T) {
	cases := []struct {
		status string
		want   EventType
	}{
		{"2", EventAcctStop},
		{"3", EventAcctUpdate},
		{"1", EventAcctStart},
	}
	for _, tc := range cases {
		entry, err := ParseAccountingEvent(
			tc.status, "sess-num", "aa:bb:cc:dd:ee:ff", "",
			"0", "0", "0", "", "",
		)
		if err != nil {
			t.Errorf("status %q: unexpected error: %v", tc.status, err)
			continue
		}
		if entry == nil {
			t.Errorf("status %q: expected entry, got nil", tc.status)
			continue
		}
		if entry.EventType != tc.want {
			t.Errorf("status %q: expected %q, got %q", tc.status, tc.want, entry.EventType)
		}
	}
}

func TestParseAccountingEvent_AccountingOnOff(t *testing.T) {
	cases := []struct {
		status string
		want   EventType
	}{
		{"Accounting-On", EventAcctOn},
		{"Accounting-Off", EventAcctOff},
		{"7", EventAcctOn},
		{"8", EventAcctOff},
	}
	for _, tc := range cases {
		entry, err := ParseAccountingEvent(
			tc.status, "", "nas-only", "",
			"", "", "", "", "10.0.0.1",
		)
		if err != nil {
			t.Errorf("status %q: unexpected error: %v", tc.status, err)
			continue
		}
		if entry == nil {
			t.Errorf("status %q: expected entry, got nil", tc.status)
			continue
		}
		if entry.EventType != tc.want {
			t.Errorf("status %q: expected %q, got %q", tc.status, tc.want, entry.EventType)
		}
	}
}

func TestParseAccountingEvent_MissingOptionalFields(t *testing.T) {
	// All optional numeric fields are empty strings — should parse to zero.
	entry, err := ParseAccountingEvent(
		"Start", "sess-empty", "aa:bb:cc:dd:ee:ff", "",
		"", "", "", "", "",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.SessionTime != 0 {
		t.Errorf("expected session_time 0, got %d", entry.SessionTime)
	}
	if entry.InputOctets != 0 {
		t.Errorf("expected input_octets 0, got %d", entry.InputOctets)
	}
	if entry.OutputOctets != 0 {
		t.Errorf("expected output_octets 0, got %d", entry.OutputOctets)
	}
	if entry.TerminateCause != "" {
		t.Errorf("expected empty terminate_cause, got %q", entry.TerminateCause)
	}
	if entry.NASIP != "" {
		t.Errorf("expected empty NAS IP, got %q", entry.NASIP)
	}
}

func TestParseAccountingEvent_InvalidNumericFields(t *testing.T) {
	// Invalid numeric strings should silently zero, not error.
	entry, err := ParseAccountingEvent(
		"Stop", "sess-bad", "aa:bb:cc:dd:ee:ff", "",
		"not-a-number", "abc", "xyz", "Lost-Carrier", "10.0.0.1",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.SessionTime != 0 {
		t.Errorf("expected session_time 0, got %d", entry.SessionTime)
	}
	if entry.InputOctets != 0 {
		t.Errorf("expected input_octets 0, got %d", entry.InputOctets)
	}
	if entry.OutputOctets != 0 {
		t.Errorf("expected output_octets 0, got %d", entry.OutputOctets)
	}
}

func TestParseAccountingEvent_EmptyMAC(t *testing.T) {
	entry, err := ParseAccountingEvent(
		"Start", "sess-nomac", "", "user1",
		"", "", "", "", "10.0.0.1",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.MAC != "" {
		t.Errorf("expected empty MAC, got %q", entry.MAC)
	}
}

func TestParseAccountingEvent_LargeOctetValues(t *testing.T) {
	// Simulates Acct-Input-Gigawords: values up to 2^64-1 (uint64 max).
	// Acct-Input-Gigawords × 2^32 + Acct-Input-Octets can exceed int32 range.
	// ParseAccountingEvent uses int64, which handles up to 9.2 × 10^18.
	largeValue := "1099511627776" // 1 TiB in bytes (2^40)
	entry, err := ParseAccountingEvent(
		"Interim-Update", "sess-large", "aa:bb:cc:dd:ee:ff", "",
		"3600", largeValue, "8796093022208", "", "10.0.0.1",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.InputOctets != 1099511627776 {
		t.Errorf("expected input_octets 1099511627776, got %d", entry.InputOctets)
	}
	if entry.OutputOctets != 8796093022208 {
		t.Errorf("expected output_octets 8796093022208, got %d", entry.OutputOctets)
	}
}

func TestParseAccountingEvent_UnknownStatusType(t *testing.T) {
	entry, err := ParseAccountingEvent(
		"Unknown", "sess-unk", "aa:bb:cc:dd:ee:ff", "",
		"10", "100", "200", "", "",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry != nil {
		t.Errorf("expected nil for unknown status type, got entry with event_type %q", entry.EventType)
	}
}
