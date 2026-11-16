package sessiond

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBootstrap_Success(t *testing.T) {
	// Setup mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		// Verify request path
		if r.URL.Path != "/" {
			t.Errorf("expected path /, got %s", r.URL.Path)
		}

		// Verify MAC header
		if r.Header.Get("X-TollGate-MAC") != "test-mac-123" {
			t.Errorf("expected MAC header test-mac-123, got %s", r.Header.Get("X-TollGate-MAC"))
		}

		// Verify Content-Type
		if r.Header.Get("Content-Type") != "text/plain" {
			t.Errorf("expected Content-Type text/plain, got %s", r.Header.Get("Content-Type"))
		}

		// Return the mock Nostr event — tag set mirrors the live tollgate-rs
		// kind 1022 event (amount_sat, token_type, effective_rate, plus
		// unrecognized tags p/device-identifier that must be skipped).
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"kind": 1022,
			"tags": [
				["allotment", "480000"],
				["metric", "milliseconds"],
				["start-time", "1700000000"],
				["amount_sat", "8"],
				["token_type", "cashu"],
				["effective_rate", "60"],
				["p", "aa11...pubkey"],
				["device-identifier", "mac", "test-mac-123"]
			],
			"id": "deadbeef",
			"pubkey": "aa11...pubkey",
			"sig": "signature",
			"created_at": 1700000000,
			"content": ""
		}`))
	}))
	defer server.Close()

	// Create client and call Bootstrap
	client := NewClient(server.URL)
	token := "test-cashu-token-123"
	mac := "test-mac-123"

	state, err := client.Bootstrap(token, mac)
	if err != nil {
		t.Fatalf("Bootstrap failed: %v", err)
	}

	// Verify parsed state
	if state.AllotmentMs != 480000 {
		t.Errorf("expected AllotmentMs 480000, got %d", state.AllotmentMs)
	}

	if state.Metric != "milliseconds" {
		t.Errorf("expected Metric milliseconds, got %s", state.Metric)
	}

	if state.StartTime != 1700000000 {
		t.Errorf("expected StartTime 1700000000, got %d", state.StartTime)
	}

	if state.CreditAmount != 8 {
		t.Errorf("expected CreditAmount 8 (amount_sat tag), got %d", state.CreditAmount)
	}

	if state.TokenType != "cashu" {
		t.Errorf("expected TokenType cashu, got %s", state.TokenType)
	}

	if state.EffectiveRateSecPerSat != 60 {
		t.Errorf("expected EffectiveRateSecPerSat 60, got %d", state.EffectiveRateSecPerSat)
	}
}

func TestBootstrap_ErrorResponse(t *testing.T) {
	// Setup mock server that returns 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	// Create client and call Bootstrap
	client := NewClient(server.URL)
	token := "test-cashu-token"
	mac := "test-mac"

	_, err := client.Bootstrap(token, mac)
	if err == nil {
		t.Error("expected error, got nil")
	}

	// Verify error message contains status code
	errStr := err.Error()
	if !contains(errStr, "500") {
		t.Errorf("expected error to contain '500', got: %s", errStr)
	}
}

func TestBootstrap_PaymentRejected(t *testing.T) {
	// Real server rejects a bad/spent token with 402 + JSON error body.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"error":"payment rejected: token already spent","session_id":"test-mac"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)

	_, err := client.Bootstrap("test-cashu-token", "test-mac")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "402") {
		t.Errorf("expected error to contain '402', got: %s", err.Error())
	}
}

func TestBootstrap_InvalidJSON(t *testing.T) {
	// Setup mock server that returns malformed JSON
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{invalid json}"))
	}))
	defer server.Close()

	// Create client and call Bootstrap
	client := NewClient(server.URL)
	token := "test-cashu-token"
	mac := "test-mac"

	_, err := client.Bootstrap(token, mac)
	if err == nil {
		t.Error("expected error, got nil")
	}

	// Verify error mentions JSON parsing
	errStr := err.Error()
	if !contains(errStr, "parsing") && !contains(errStr, "JSON") {
		t.Errorf("expected error to mention JSON parsing, got: %s", errStr)
	}
}

func TestBootstrap_WrongKind(t *testing.T) {
	// Setup mock server that returns kind 1 instead of 1022
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		event := nostrEvent{
			Kind: 1, // Wrong kind
			Tags: [][]string{
				{"allotment", "480000"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(event)
	}))
	defer server.Close()

	// Create client and call Bootstrap
	client := NewClient(server.URL)
	token := "test-cashu-token"
	mac := "test-mac"

	_, err := client.Bootstrap(token, mac)
	if err == nil {
		t.Error("expected error, got nil")
	}

	// Verify error mentions kind
	errStr := err.Error()
	if !contains(errStr, "1022") && !contains(errStr, "kind") {
		t.Errorf("expected error to mention kind 1022, got: %s", errStr)
	}
}

func TestGetUsage_Success(t *testing.T) {
	// Setup mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}

		// Verify request path
		if r.URL.Path != "/usage" {
			t.Errorf("expected path /usage, got %s", r.URL.Path)
		}

		// Verify MAC header
		if r.Header.Get("X-TollGate-MAC") != "test-mac-456" {
			t.Errorf("expected MAC header test-mac-456, got %s", r.Header.Get("X-TollGate-MAC"))
		}

		// Return plain text usage
		w.Write([]byte("120000/480000"))
	}))
	defer server.Close()

	// Create client and call GetUsage
	client := NewClient(server.URL)
	mac := "test-mac-456"

	usage, err := client.GetUsage(mac)
	if err != nil {
		t.Fatalf("GetUsage failed: %v", err)
	}

	// Verify usage string
	if usage != "120000/480000" {
		t.Errorf("expected usage '120000/480000', got '%s'", usage)
	}
}

func TestGetUsage_ErrorResponse(t *testing.T) {
	// Setup mock server that returns 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	// Create client and call GetUsage
	client := NewClient(server.URL)
	mac := "test-mac"

	_, err := client.GetUsage(mac)
	if err == nil {
		t.Error("expected error, got nil")
	}

	// Verify error message contains status code
	errStr := err.Error()
	if !contains(errStr, "500") {
		t.Errorf("expected error to contain '500', got: %s", errStr)
	}
}

// Helper function to check if string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestGetSession_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v1/sessions/aa:bb:cc:dd:ee:ff" {
			t.Errorf("expected path /v1/sessions/aa:bb:cc:dd:ee:ff, got %s", r.URL.Path)
		}
		if r.Header.Get("X-TollGate-MAC") != "aa-bb-cc-dd-ee-ff" {
			t.Errorf("expected MAC header aa-bb-cc-dd-ee-ff, got %s", r.Header.Get("X-TollGate-MAC"))
		}
		// Real GET /v1/sessions/{mac} response: numbers as JSON numbers,
		// session_id echoes the MAC, last_usage_update omitted when no
		// usage has been reported yet.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"session_id":"aa:bb:cc:dd:ee:ff","access_level":"active","allotment":480000,"remaining_quota":360000,"metric":"milliseconds","next_checkin_ms":5000,"is_final":false,"created_at":1700000000}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	state, err := client.GetSession("aa-bb-cc-dd-ee-ff")
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if state.SessionID != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("expected SessionID aa:bb:cc:dd:ee:ff, got %s", state.SessionID)
	}
	if state.AccessLevel != "active" {
		t.Errorf("expected AccessLevel active, got %s", state.AccessLevel)
	}
	if state.RemainingQuota != 360000 {
		t.Errorf("expected RemainingQuota 360000, got %d", state.RemainingQuota)
	}
	if state.NextCheckinMs != 5000 {
		t.Errorf("expected NextCheckinMs 5000, got %d", state.NextCheckinMs)
	}
	if state.CreatedAt != 1700000000 {
		t.Errorf("expected CreatedAt 1700000000, got %d", state.CreatedAt)
	}
	if state.LastUsageUpdate != "" {
		t.Errorf("expected LastUsageUpdate empty (omitted), got %q", state.LastUsageUpdate)
	}
}

func TestGetSession_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"session not found","session_id":"unknown-mac"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	_, err := client.GetSession("unknown-mac")
	if err == nil {
		t.Error("expected error, got nil")
	}
	errStr := err.Error()
	if !contains(errStr, "404") {
		t.Errorf("expected error to contain '404', got: %s", errStr)
	}
}

func TestReportUsage_Success(t *testing.T) {
	var receivedBody UsageReport

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/sessions/aa:bb:cc:dd:ee:ff/usage" {
			t.Errorf("expected path /v1/sessions/aa:bb:cc:dd:ee:ff/usage, got %s", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-api-key" {
			t.Errorf("expected X-API-Key test-api-key, got %s", r.Header.Get("X-API-Key"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}

		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}

		// Real usage-report response: last_usage_update is a unix-seconds
		// STRING; every other timestamp is a JSON number.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"session_id":"aa:bb:cc:dd:ee:ff","access_level":"active","allotment":480000,"remaining_quota":240000,"metric":"milliseconds","next_checkin_ms":5000,"is_final":false,"created_at":1700000000,"last_usage_update":"1700000120"}`))
	}))
	defer server.Close()

	inputOctets := uint64(1048576)
	outputOctets := uint64(524288)
	sessionTime := uint64(120)

	client := NewClient(server.URL)
	report := UsageReport{
		InputOctets:  &inputOctets,
		OutputOctets: &outputOctets,
		SessionTime:  &sessionTime,
		Source:       "radius-accounting",
		Timestamp:    "2025-01-01T00:00:00Z",
	}

	state, err := client.ReportUsage("aa-bb-cc-dd-ee-ff", report, "test-api-key")
	if err != nil {
		t.Fatalf("ReportUsage failed: %v", err)
	}
	if state.AccessLevel != "active" {
		t.Errorf("expected AccessLevel active, got %s", state.AccessLevel)
	}
	if state.RemainingQuota != 240000 {
		t.Errorf("expected RemainingQuota 240000, got %d", state.RemainingQuota)
	}
	if state.CreatedAt != 1700000000 {
		t.Errorf("expected CreatedAt 1700000000 (JSON number), got %d", state.CreatedAt)
	}
	if state.LastUsageUpdate != "1700000120" {
		t.Errorf("expected LastUsageUpdate \"1700000120\" (string), got %q", state.LastUsageUpdate)
	}
	if receivedBody.Source != "radius-accounting" {
		t.Errorf("expected source radius-accounting, got %s", receivedBody.Source)
	}
	if receivedBody.InputOctets == nil || *receivedBody.InputOctets != 1048576 {
		t.Errorf("expected InputOctets 1048576, got %v", receivedBody.InputOctets)
	}
	if receivedBody.SessionTime == nil || *receivedBody.SessionTime != 120 {
		t.Errorf("expected SessionTime 120, got %v", receivedBody.SessionTime)
	}
}

func TestReportUsage_Suspended(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := SessionResponse{
			SessionID:      "test-session-1",
			AccessLevel:    "suspended",
			RemainingQuota: 0,
			IsFinal:        true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	sessionTime := uint64(480)
	report := UsageReport{
		SessionTime: &sessionTime,
		Source:      "radius-accounting",
	}

	state, err := client.ReportUsage("aa-bb-cc-dd-ee-ff", report, "test-api-key")
	if err != nil {
		t.Fatalf("ReportUsage failed: %v", err)
	}
	if state.AccessLevel != "suspended" {
		t.Errorf("expected AccessLevel suspended, got %s", state.AccessLevel)
	}
	if !state.IsFinal {
		t.Error("expected IsFinal true")
	}
}

func TestReportUsage_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	report := UsageReport{Source: "radius-accounting"}
	_, err := client.ReportUsage("test-mac", report, "test-api-key")
	if err == nil {
		t.Error("expected error, got nil")
	}
	errStr := err.Error()
	if !contains(errStr, "500") {
		t.Errorf("expected error to contain '500', got: %s", errStr)
	}
}

func TestReportUsage_Unauthorized(t *testing.T) {
	// Real server answers a missing/failed X-API-Key with 401 + JSON error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	report := UsageReport{Source: "radius-accounting"}

	_, err := client.ReportUsage("aa-bb-cc-dd-ee-ff", report, "wrong-api-key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "401") {
		t.Errorf("expected error to contain '401', got: %s", err.Error())
	}
}
