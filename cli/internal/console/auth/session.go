package auth

import "context"

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
	var out *Session
	if err := c.api.Get(ctx, "/api/auth/get-session", accessToken, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) SignOut(ctx context.Context, accessToken string) error {
	return c.api.Post(ctx, "/api/auth/sign-out", accessToken, struct{}{}, nil)
}
