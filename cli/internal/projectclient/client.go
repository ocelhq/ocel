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

type Project struct {
	ID             string  `json:"id"`
	OrganizationID string  `json:"organizationId"`
	Name           string  `json:"name"`
	Slug           string  `json:"slug"`
	Description    *string `json:"description"`
}

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type apiError struct {
	Error string `json:"error"`
}

type APIError struct {
	Message    string
	StatusCode int
}

func (e *APIError) Error() string {
	return e.Message
}

func IsConflict(err error) bool {
	var apiErr *APIError
	return asAPIError(err, &apiErr) && apiErr.StatusCode == http.StatusConflict
}

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

func (c *Client) getJSON(ctx context.Context, path, accessToken string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "ocel-cli")

	return c.do(req, out)
}

func (c *Client) ListProjects(ctx context.Context, accessToken string) ([]Project, error) {
	var projects []Project
	if err := c.getJSON(ctx, "/api/projects", accessToken, &projects); err != nil {
		return nil, err
	}
	return projects, nil
}

func (c *Client) CreateProject(ctx context.Context, accessToken, name, slug string) (*Project, error) {
	var project Project
	body := map[string]string{"name": name, "slug": slug}
	if err := c.postJSON(ctx, "/api/projects", accessToken, body, &project); err != nil {
		return nil, err
	}
	return &project, nil
}
