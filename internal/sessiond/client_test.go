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

		// Return the mock Nostr event
		event := nostrEvent{
			Kind: 1022,
			Tags: [][]string{
				{"allotment", "480000"},
				{"metric", "milliseconds"},
				{"start-time", "1700000000"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(event)
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
