// Package authclient is a minimal HTTP client for the subset of Better
// Auth's REST API the Ocel CLI needs: the device authorization grant
// (RFC 8628) and session lookups/sign-out via Bearer token.
package authclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ClientID is the fixed OAuth client identifier the Ocel CLI presents to
// the device authorization endpoint. The server validates it via
// deviceAuthorization({ validateClient }) in apps/web/lib/auth.ts.
const ClientID = "ocel-cli"

// Client talks to a Better Auth server's device authorization + session
// endpoints.
type Client struct {
	// BaseURL is the origin of the Ocel server, e.g. http://localhost:3000.
	BaseURL string
	// HTTPClient is used for all requests. Defaults to a client with a
	// sane timeout if left nil via New.
	HTTPClient *http.Client
}

// New constructs a Client for the given base URL.
func New(baseURL string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// apiError mirrors Better Auth's { error, error_description } error body
// shape, shared by the device/code and device/token endpoints.
type apiError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// postJSON POSTs body as JSON to path and decodes a JSON response into out.
// On a non-2xx response it attempts to decode an apiError and returns it
// wrapped as an *APIError; if that fails it returns a generic error with the
// raw response body for debuggability.
func (c *Client) postJSON(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ocel-cli")

	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("reach %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr apiError
		if jsonErr := json.Unmarshal(data, &apiErr); jsonErr == nil && apiErr.Error != "" {
			return &APIError{Code: apiErr.Error, Description: apiErr.ErrorDescription, StatusCode: resp.StatusCode}
		}
		return fmt.Errorf("unexpected response (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// APIError represents a structured error returned by a Better Auth
// endpoint, e.g. {"error":"authorization_pending","error_description":"..."}.
type APIError struct {
	Code        string
	Description string
	StatusCode  int
}

func (e *APIError) Error() string {
	if e.Description != "" {
		return e.Description
	}
	return e.Code
}
