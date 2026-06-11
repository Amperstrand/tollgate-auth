package sessiond

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SessionState represents the session data returned by the v1 server.
type SessionState struct {
	AllotmentMs uint64 // allotment in milliseconds (parsed from Nostr event tags)
	Metric      string // "milliseconds" or "bytes"
	StartTime   int64  // unix timestamp
}

// nostrEvent represents a minimal Nostr event for parsing the session response.
type nostrEvent struct {
	Kind int      `json:"kind"`
	Tags [][]string `json:"tags"`
}

// Client is an HTTP client for the tollgate-net v1 server.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewClient creates a new client pointing at the v1 server.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: 35 * time.Second},
	}
}

// Bootstrap sends a Cashu token to the v1 server and returns the session allotment.
// It uses the existing POST / endpoint with the raw Cashu token as body
// and X-TollGate-MAC header for MAC address.
func (c *Client) Bootstrap(token string, mac string) (*SessionState, error) {
	req, err := http.NewRequest("POST", c.BaseURL+"/", strings.NewReader(token))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("X-TollGate-MAC", mac)
	req.Header.Set("Content-Type", "text/plain")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("v1 server returned %d: %s", resp.StatusCode, string(body))
	}

	// Parse the Nostr kind 1022 session event
	var event nostrEvent
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("parsing session event: %w", err)
	}

	if event.Kind != 1022 {
		return nil, fmt.Errorf("unexpected event kind %d, expected 1022", event.Kind)
	}

	state := &SessionState{}
	for _, tag := range event.Tags {
		if len(tag) >= 2 {
			switch tag[0] {
			case "allotment":
				val, err := strconv.ParseUint(tag[1], 10, 64)
				if err != nil {
					return nil, fmt.Errorf("parsing allotment: %w", err)
				}
				state.AllotmentMs = val
			case "metric":
				state.Metric = tag[1]
			case "start-time":
				val, err := strconv.ParseInt(tag[1], 10, 64)
				if err != nil {
					return nil, fmt.Errorf("parsing start-time: %w", err)
				}
				state.StartTime = val
			}
		}
	}

	return state, nil
}

// GetUsage queries the current usage for a MAC address.
// Returns "used/allotment" string (e.g. "120000/480000") or error.
func (c *Client) GetUsage(mac string) (string, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/usage", nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("X-TollGate-MAC", mac)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("v1 server returned %d: %s", resp.StatusCode, string(body))
	}

	return strings.TrimSpace(string(body)), nil
}
