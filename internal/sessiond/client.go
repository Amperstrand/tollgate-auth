package sessiond

import (
	"bytes"
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
	AllotmentMs            uint64 // allotment in milliseconds (parsed from Nostr event tags)
	Metric                 string // "milliseconds" or "bytes"
	StartTime              int64  // unix timestamp
	AmountSat              uint64 // raw token amount in sats (0 if not provided by legacy server)
	TokenType              string // "cashu" or "lnurlw" ("" if not provided by legacy server)
	EffectiveRateSecPerSat uint64 // seconds per sat: allotment_seconds / amount_sat (0 if not provided)
}

// nostrEvent represents a minimal Nostr event for parsing the session response.
type nostrEvent struct {
	Kind int        `json:"kind"`
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
			case "amount_sat":
				val, err := strconv.ParseUint(tag[1], 10, 64)
				if err != nil {
					return nil, fmt.Errorf("parsing amount_sat: %w", err)
				}
				state.AmountSat = val
			case "token_type":
				state.TokenType = tag[1]
			case "effective_rate":
				val, err := strconv.ParseUint(tag[1], 10, 64)
				if err != nil {
					return nil, fmt.Errorf("parsing effective_rate: %w", err)
				}
				state.EffectiveRateSecPerSat = val
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

// SessionResponse represents session state returned by the session daemon.
type SessionResponse struct {
	SessionID       string `json:"session_id"`
	AccessLevel     string `json:"access_level"`    // "active", "suspended", "restricted"
	Allotment       int64  `json:"allotment"`       // total allotment in metric units
	RemainingQuota  int64  `json:"remaining_quota"` // remaining quota in metric units
	Metric          string `json:"metric"`          // "milliseconds" or "bytes"
	NextCheckinMs   int64  `json:"next_checkin_ms"`
	IsFinal         bool   `json:"is_final"`
	CreatedAt       int64  `json:"created_at"` // unix timestamp
	LastUsageUpdate string `json:"last_usage_update,omitempty"`
}

// UsageReport represents a normalized usage event from RADIUS accounting.
type UsageReport struct {
	InputOctets  *uint64 `json:"input_octets,omitempty"`
	OutputOctets *uint64 `json:"output_octets,omitempty"`
	SessionTime  *uint64 `json:"session_time,omitempty"`
	Source       string  `json:"source"`              // e.g. "radius-accounting"
	Timestamp    string  `json:"timestamp,omitempty"` // RFC 3339
}

// GetSession retrieves session state from the session daemon via
// GET /v1/session with X-TollGate-MAC header.
func (c *Client) GetSession(mac string) (*SessionResponse, error) {
	normalizedMAC := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(mac, "-", ":"), ".", ":"))
	req, err := http.NewRequest("GET", c.BaseURL+"/v1/sessions/"+normalizedMAC, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("X-TollGate-MAC", mac)

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
		return nil, fmt.Errorf("session daemon returned %d: %s", resp.StatusCode, string(body))
	}

	var state SessionResponse
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, fmt.Errorf("parsing session response: %w", err)
	}
	return &state, nil
}

// ReportUsage sends a usage report to the session daemon via
// POST /v1/sessions/{mac}/usage with JSON body and X-API-Key header.
// Returns updated session state for CoA enforcement decisions.
func (c *Client) ReportUsage(mac string, report UsageReport, apiKey string) (*SessionResponse, error) {
	payload, err := json.Marshal(report)
	if err != nil {
		return nil, fmt.Errorf("marshaling usage report: %w", err)
	}

	normalizedMAC := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(mac, "-", ":"), ".", ":"))
	url := c.BaseURL + "/v1/sessions/" + normalizedMAC + "/usage"

	req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)

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
		return nil, fmt.Errorf("session daemon returned %d: %s", resp.StatusCode, string(body))
	}

	var state SessionResponse
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, fmt.Errorf("parsing session response: %w", err)
	}
	return &state, nil
}
