package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tollgate-auth/internal/ledger"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
)

// Known-valid nsec (bech32-encoded secret key) and a known-valid npub
// (bech32-encoded public key), both taken from the nip19 test vectors in
// fiatjaf.com/nostr. They are independent test vectors — the npub is NOT the
// public key derived from the nsec; we compute that separately below.
const (
	testNsec = "nsec180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsgyumg0"
	testNpub = "npub180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsyjh6w6"
)

// TestNotificationPayload_ContainsNoPII verifies that the exact JSON byte slice
// handed to nip17.PublishMessage as the DM content carries zero PII even when
// the source ledger is full of MACs, IPs, and token hashes.
func TestNotificationPayload_ContainsNoPII(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notify.jsonl")
	l, err := ledger.OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer l.Close()

	const operatorID = "op-notify"
	now := time.Now().UTC()
	// Seed the ledger with entries containing every kind of PII.
	entries := []ledger.LedgerEntry{
		{
			EventType:    ledger.EventAuthAccept,
			OperatorID:   operatorID,
			MAC:          "aa:bb:cc:dd:ee:ff",
			PaymentType:  "cashu",
			AmountSat:    12,
			DurationSec:  720,
			MintURL:      "https://testnut.cashu.space",
			TokenHash:    "secrettokenhash001",
			NASIP:        "10.0.0.7",
			ReplyMessage: "Valid Cashu token: 12 sat",
			Timestamp:    now.Add(-1 * time.Hour).Format(time.RFC3339),
		},
		{
			EventType:    ledger.EventAuthAccept,
			OperatorID:   operatorID,
			MAC:          "11:22:33:44:55:66",
			PaymentType:  "lnurlw",
			AmountSat:    8,
			DurationSec:  480,
			TokenHash:    "secrettokenhash002",
			NASIP:        "192.168.1.1",
			ReplyMessage: "Valid LNURLW: 8 sat",
			Timestamp:    now.Add(-30 * time.Minute).Format(time.RFC3339),
		},
		{
			EventType:    ledger.EventAuthReject,
			OperatorID:   operatorID,
			MAC:          "ff:ee:dd:cc:bb:aa",
			PaymentType:  "cashu",
			TokenHash:    "rejectedtokenhash003",
			NASIP:        "172.16.0.1",
			ReplyMessage: "Replay detected",
			Timestamp:    now.Add(-15 * time.Minute).Format(time.RFC3339),
		},
	}
	for _, e := range entries {
		if err := l.RecordAuth(e); err != nil {
			t.Fatalf("RecordAuth: %v", err)
		}
	}

	report, err := BuildSettlementReport(l, operatorID, now.Add(-24*time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("BuildSettlementReport: %v", err)
	}

	// This is the exact payload handed to nip17.PublishMessage as content.
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	payloadStr := strings.ToLower(string(payload))

	// Every PII atom present in the source ledger must be absent from the payload.
	piiAtoms := []string{
		"aa:bb:cc",          // accepted MAC #1
		"11:22:33",          // accepted MAC #2
		"ff:ee:dd",          // rejected MAC
		"10.0.0.7",          // NAS IP
		"192.168.1.1",       // NAS IP
		"172.16.0.1",        // NAS IP
		"secrettokenhash",   // token hashes
		"rejectedtokenhash", // token hash
		"token_hash",        // field names that must not exist
		"nas_ip",
		"acct_session_id",
		"session_class",
		"terminate_cause",
		"input_octets",
		"output_octets",
		"mint_url",
		"reply_message",
		"cashu",  // payment type
		"lnurlw", // payment type
		"mac",    // generic mac field name
	}
	for _, atom := range piiAtoms {
		if strings.Contains(payloadStr, atom) {
			t.Errorf("notification payload must not contain %q, but does:\n%s", atom, string(payload))
		}
	}

	// Aggregates must survive.
	if report.TotalSat != 20 {
		t.Errorf("total_sat = %d, want 20", report.TotalSat)
	}
	if report.AcceptedSessions != 2 {
		t.Errorf("accepted_sessions = %d, want 2", report.AcceptedSessions)
	}
	if report.RejectedSessions != 1 {
		t.Errorf("rejected_sessions = %d, want 1", report.RejectedSessions)
	}
}

// TestDecodeKeys_Valid checks that decodeNsec/decodeNpub round-trip against the
// nip19 reference implementation for the known test vector.
func TestDecodeKeys_Valid(t *testing.T) {
	sk, err := decodeNsec(testNsec)
	if err != nil {
		t.Fatalf("decodeNsec: %v", err)
	}
	// Re-encode and compare to the input to prove round-trip fidelity.
	if got := nip19.EncodeNsec(sk); got != testNsec {
		t.Errorf("EncodeNsec round-trip = %q, want %q", got, testNsec)
	}

	pk, err := decodeNpub(testNpub)
	if err != nil {
		t.Fatalf("decodeNpub: %v", err)
	}
	if got := nip19.EncodeNpub(pk); got != testNpub {
		t.Errorf("EncodeNpub round-trip = %q, want %q", got, testNpub)
	}

	// The public key derived from the secret key must round-trip through
	// npub encode/decode. (testNpub is an independent vector, not this key.)
	derivedPK := nostr.GetPublicKey(sk)
	derivedNpub := nip19.EncodeNpub(derivedPK)
	roundTripPK, err := decodeNpub(derivedNpub)
	if err != nil {
		t.Fatalf("decodeNpub(derived): %v", err)
	}
	if roundTripPK != derivedPK {
		t.Errorf("derived npub round-trip mismatch: %s != %s", roundTripPK.Hex(), derivedPK.Hex())
	}
}

// TestDecodeKeys_InvalidNsec verifies that malformed nsec inputs are rejected.
func TestDecodeKeys_InvalidNsec(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"garbage", "not-a-bech32-string"},
		{"wrong_prefix", testNpub}, // npub passed where nsec expected
		{"bad_checksum", "nsec180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsgyumg4"},
		{"truncated", "nsec1abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeNsec(tc.input); err == nil {
				t.Errorf("decodeNsec(%q) = nil error, want error", tc.input)
			}
		})
	}
}

// TestDecodeKeys_InvalidNpub verifies that malformed npub inputs are rejected.
func TestDecodeKeys_InvalidNpub(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"garbage", "!!!"},
		{"wrong_prefix", testNsec}, // nsec passed where npub expected
		{"bad_checksum", "npub180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsyjh6w4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeNpub(tc.input); err == nil {
				t.Errorf("decodeNpub(%q) = nil error, want error", tc.input)
			}
		})
	}
}

// TestParseRelays exercises the relay list parser used to build the nip17
// ourRelays/theirRelays arguments.
func TestParseRelays(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty uses default", "", defaultRelays},
		{"single", "wss://r.example", []string{"wss://r.example"}},
		{"comma with spaces", "wss://a.example , wss://b.example", []string{"wss://a.example", "wss://b.example"}},
		{"trailing comma", "wss://a.example,", []string{"wss://a.example"}},
		{"only whitespace uses default", "   ", defaultRelays},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRelays(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parseRelays(%q) = %v (len %d), want %v (len %d)", tc.in, got, len(got), tc.want, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("parseRelays(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}
