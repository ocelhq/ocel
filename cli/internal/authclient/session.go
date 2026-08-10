package authclient

import (
	"context"
	"net/http"
	"strings"
)

type Session struct {
	Session struct {
		ExpiresAt string `json:"expiresAt"`
	} `json:"session"`
	User struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"user"`
}

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
