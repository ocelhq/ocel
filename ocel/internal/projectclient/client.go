// Package projectclient is a minimal HTTP client for the Ocel control
// plane's own REST API (packages/api/src/projects.ts) — not a Better Auth
// endpoint, so it's kept separate from internal/authclient rather than
// unified with it.
package projectclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Project mirrors the row JSON-encoded by POST /api/projects on success.
type Project struct {
	ID             string  `json:"id"`
	OrganizationID string  `json:"organizationId"`
	Name           string  `json:"name"`
	Slug           string  `json:"slug"`
	Description    *string `json:"description"`
}

// Client talks to the Ocel control plane's /api/projects endpoints.
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

// apiError mirrors this API's plain {"error": "..."} error body shape —
// distinct from Better Auth's {error, error_description} shape used by
// internal/authclient.
type apiError struct {
	Error string `json:"error"`
}

// APIError represents a structured error returned by the projects API.
type APIError struct {
	Message    string
	StatusCode int
}

func (e *APIError) Error() string {
	return e.Message
}

// IsConflict reports whether err represents a 409 (slug already taken)
// response.
func IsConflict(err error) bool {
	var apiErr *APIError
	return asAPIError(err, &apiErr) && apiErr.StatusCode == http.StatusConflict
}

// IsUnauthorized reports whether err represents a 401 response.
func IsUnauthorized(err error) bool {
	var apiErr *APIError
	return asAPIError(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized
}

func asAPIError(err error, target **APIError) bool {
	apiErr, ok := err.(*APIError)
	if !ok {
		return false
	}
	*target = apiErr
	return true
}

// postJSON POSTs body as JSON to path with accessToken as a Bearer token,
// and decodes a JSON response into out.
func (c *Client) postJSON(ctx context.Context, path, accessToken string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
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
			return &APIError{Message: apiErr.Error, StatusCode: resp.StatusCode}
		}
		return &APIError{
			Message:    fmt.Sprintf("unexpected response (%d): %s", resp.StatusCode, strings.TrimSpace(string(data))),
			StatusCode: resp.StatusCode,
		}
	}

	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// CreateProject calls POST /api/projects with {"name": name, "slug": slug}
// and the given Bearer accessToken.
func (c *Client) CreateProject(ctx context.Context, accessToken, name, slug string) (*Project, error) {
	var project Project
	body := map[string]string{"name": name, "slug": slug}
	if err := c.postJSON(ctx, "/api/projects", accessToken, body, &project); err != nil {
		return nil, err
	}
	return &project, nil
}
