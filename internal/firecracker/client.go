// Package firecracker provides an HTTP client for the vps-on-demand
// Firecracker daemon API. It handles VM lifecycle: create, destroy.
//
// The daemon runs at http://127.0.0.1:8081 on the KVM host.
// See: https://github.com/Amperstrand/vps-on-demand
package firecracker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to the firecracker-daemon HTTP API.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewClient creates a client for the daemon at baseURL
// (e.g. "http://127.0.0.1:8081").
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// VMSpec specifies the VM to create.
type VMSpec struct {
	CPUs       int    `json:"cpus"`
	MemMB      int    `json:"mem_mb"`
	DiskMB     int    `json:"disk_mb"`
	SSHKey     string `json:"ssh_key,omitempty"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

// VMResponse is the daemon's response to POST /vms.
type VMResponse struct {
	ID         string `json:"id"`
	IP         string `json:"ip"`
	PublicPort int    `json:"public_port"`
	CPUs       int    `json:"cpus"`
	MemMB      int    `json:"mem_mb"`
	DiskMB     int    `json:"disk_mb"`
	SSH        string `json:"ssh,omitempty"`
	Password   string `json:"password,omitempty"`
	Tap        string `json:"tap,omitempty"`
}

// CreateVM creates a new Firecracker microVM.
func (c *Client) CreateVM(spec VMSpec) (*VMResponse, error) {
	body, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("marshal VM spec: %w", err)
	}

	resp, err := c.HTTPClient.Post(
		c.BaseURL+"/vms",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("POST /vms: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("POST /vms: HTTP %d: %s", resp.StatusCode, respBody)
	}

	var vmResp VMResponse
	if err := json.Unmarshal(respBody, &vmResp); err != nil {
		return nil, fmt.Errorf("parse VM response: %w", err)
	}

	return &vmResp, nil
}

// DestroyVM destroys a VM by ID.
func (c *Client) DestroyVM(vmID string) error {
	req, err := http.NewRequest("DELETE", c.BaseURL+"/vms/"+vmID, nil)
	if err != nil {
		return fmt.Errorf("create DELETE request: %w", err)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("DELETE /vms/%s: %w", vmID, err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("DELETE /vms/%s: HTTP %d", vmID, resp.StatusCode)
	}

	return nil
}

// Health checks if the daemon is running.
func (c *Client) Health() error {
	resp, err := c.HTTPClient.Get(c.BaseURL + "/health")
	if err != nil {
		return fmt.Errorf("GET /health: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET /health: HTTP %d", resp.StatusCode)
	}
	return nil
}
