package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

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

type Error struct {
	Message    string
	StatusCode int
}

func (e *Error) Error() string {
	return fmt.Sprintf("the console answered %d: %s", e.StatusCode, e.Message)
}

func HasStatus(err error, status int) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

func (c *Client) Get(ctx context.Context, path, accessToken string, out any) error {
	return c.do(ctx, http.MethodGet, path, accessToken, nil, out)
}

func (c *Client) Delete(ctx context.Context, path, accessToken string, out any) error {
	return c.do(ctx, http.MethodDelete, path, accessToken, nil, out)
}

func (c *Client) Post(ctx context.Context, path, accessToken string, body, out any) error {
	return c.withJSON(ctx, http.MethodPost, path, accessToken, body, out)
}

func (c *Client) Put(ctx context.Context, path, accessToken string, body, out any) error {
	return c.withJSON(ctx, http.MethodPut, path, accessToken, body, out)
}

func (c *Client) withJSON(ctx context.Context, method, path, accessToken string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	return c.do(ctx, method, path, accessToken, payload, out)
}

func (c *Client) do(ctx context.Context, method, path, accessToken string, body []byte, out any) error {
	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, payload)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "ocel-cli")

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
		return statusError(resp.StatusCode, data)
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func statusError(status int, data []byte) error {
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &body); err == nil && body.Error != "" {
		return &Error{Message: body.Error, StatusCode: status}
	}
	return &Error{Message: strings.TrimSpace(string(data)), StatusCode: status}
}
