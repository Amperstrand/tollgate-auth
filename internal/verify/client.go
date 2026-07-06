// Package verify is a client for the shared Rust Cashu verifier (tollgate-rs
// POST /verify). It lets Go services verify Cashu via one HTTP call instead of
// a cdk-cli subprocess, so Cashu handling can live in the Rust verifier.
package verify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// DefaultURL is the tollgate-rs v1-server verify endpoint on loopback.
const DefaultURL = "http://127.0.0.1:2121/verify"

// Client calls the shared Rust verifier.
type Client struct {
	url  string
	http *http.Client
}

// NewClient returns a verifier client. Empty url falls back to DefaultURL.
func NewClient(url string) *Client {
	if url == "" {
		url = DefaultURL
	}
	return &Client{url: url, http: &http.Client{Timeout: 35 * time.Second}}
}

type verifyRequest struct {
	Token string `json:"token"`
}

type verifyResponse struct {
	Ok     bool   `json:"ok"`
	Amount uint64 `json:"amount"`
	Code   string `json:"code"`
	Error  string `json:"error"`
}

// Verified is the result of a successful verification.
type Verified struct {
	Amount uint64
}

// Verify submits a Cashu token to the Rust verifier, which decodes and redeems
// it via the mint (the mint is the authority for spent/replay). On success the
// proofs have been received (swapped) and Amount holds the received sats.
func (c *Client) Verify(token string) (Verified, error) {
	if token == "" {
		return Verified{}, errors.New("empty token")
	}
	body, err := json.Marshal(verifyRequest{Token: token})
	if err != nil {
		return Verified{}, fmt.Errorf("marshal request: %w", err)
	}
	resp, err := c.http.Post(c.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return Verified{}, fmt.Errorf("verifier unreachable: %w", err)
	}
	defer resp.Body.Close()

	var r verifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return Verified{}, fmt.Errorf("verifier response parse: %w", err)
	}
	if !r.Ok {
		code := r.Code
		if code == "" {
			code = "verify-error"
		}
		return Verified{}, &RejectedError{Code: code, Detail: r.Error}
	}
	return Verified{Amount: r.Amount}, nil
}

// RejectedError is returned when the verifier rejects the token.
type RejectedError struct {
	Code   string
	Detail string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("verifier rejected: %s (%s)", e.Code, e.Detail)
}

// IsSpent reports whether the rejection was a double-spend.
func (e *RejectedError) IsSpent() bool {
	return e.Code == "payment-error-token-spent"
}
