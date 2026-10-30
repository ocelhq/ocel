package console

import (
	"context"
	"net/http"
	"time"

	"github.com/ocelhq/ocel/pkg/tracing/content"
)

type Session struct {
	Session struct {
		ExpiresAt            string `json:"expiresAt"`
		ActiveOrganizationID string `json:"activeOrganizationId"`
	} `json:"session"`
	User struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"user"`
}

func (c *Client) GetSession(ctx context.Context, accessToken string) (*Session, error) {
	var out *Session
	if err := c.send(content.Omit(ctx), http.MethodGet, "/api/auth/get-session", accessToken, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) SignOut(ctx context.Context, accessToken string) error {
	return c.send(ctx, http.MethodPost, "/api/auth/sign-out", accessToken, struct{}{}, nil)
}

func ResolveSession(load func() (Credentials, error), now time.Time) (baseURL, accessToken string) {
	if load == nil {
		return BaseURL(""), ""
	}
	creds, err := load()
	if err != nil {
		return BaseURL(""), ""
	}
	baseURL = BaseURL(creds.APIURL)
	issuedHere := creds.Store == EnvironmentStore || NormalizeBaseURL(creds.APIURL) == baseURL
	current := creds.ExpiresAt.IsZero() || now.Before(creds.ExpiresAt)
	if !issuedHere || !current {
		return baseURL, ""
	}
	return baseURL, creds.AccessToken
}
