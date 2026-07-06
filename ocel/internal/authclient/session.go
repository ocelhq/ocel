package authclient

import (
	"context"
	"net/http"
	"strings"
)

// Session mirrors the subset of Better Auth's session/user payload the CLI
// cares about (GET /api/auth/get-session).
type Session struct {
	Session struct {
		ExpiresAt string `json:"expiresAt"`
	} `json:"session"`
	User struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"user"`
}

// GetSession fetches the session/user associated with accessToken. Returns
// nil, nil if the token doesn't resolve to a session (e.g. already expired
// or revoked) — Better Auth returns `null` rather than an error in that
// case.
func (c *Client) GetSession(ctx context.Context, accessToken string) (*Session, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/auth/get-session", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "ocel-cli")

	var out *Session
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SignOut revokes accessToken's session server-side. Used by `ocel logout`
// on a best-effort basis: local credentials are cleared regardless of
// whether this succeeds.
func (c *Client) SignOut(ctx context.Context, accessToken string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/auth/sign-out", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ocel-cli")

	return c.do(req, nil)
}
