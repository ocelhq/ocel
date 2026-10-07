package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/invocation"
)

const connectRoute = "/api/connect"

type Client struct {
	baseURL   string
	userAgent string
	http      *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		userAgent: composeUserAgent(os.Getenv),
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) session(accessToken string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+accessToken)
			req.Header().Set("User-Agent", c.userAgent)
			return next(ctx, req)
		}
	}))
}

func composeUserAgent(getenv func(string) string) string {
	agent, _ := invocation.Detect(getenv)
	if agent == "" {
		return "ocel-cli"
	}
	return "ocel-cli agent/" + agent
}

type Error struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("the console answered %d: %s", e.StatusCode, e.Message)
}

func hasCode(err error, code string) bool {
	var consoleErr *Error
	return errors.As(err, &consoleErr) && consoleErr.Code == code
}

func (c *Client) send(ctx context.Context, method, path, accessToken string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("reach %s: %w", c.baseURL, err)
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
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(data, &body); err == nil && body.Error != "" {
		message := body.Error
		if body.ErrorDescription != "" {
			message = body.ErrorDescription
		}
		return &Error{StatusCode: status, Code: body.Error, Message: message}
	}
	return &Error{StatusCode: status, Message: strings.TrimSpace(string(data))}
}
