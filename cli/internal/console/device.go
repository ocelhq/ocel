package console

import (
	"context"
	"net/http"
)

const clientID = "ocel-cli"

const (
	codeAuthorizationPending = "authorization_pending"
	codeSlowDown             = "slow_down"
	codeAccessDenied         = "access_denied"
	codeExpiredToken         = "expired_token"
)

type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func (c *Client) RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	var out DeviceCode
	body := map[string]string{"client_id": clientID}
	if err := c.send(ctx, http.MethodPost, "/api/auth/device/code", "", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type Token struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

func (c *Client) PollToken(ctx context.Context, deviceCode string) (*Token, error) {
	var out Token
	body := map[string]string{
		"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
		"device_code": deviceCode,
		"client_id":   clientID,
	}
	if err := c.send(ctx, http.MethodPost, "/api/auth/device/token", "", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func IsAuthorizationPending(err error) bool {
	return hasCode(err, codeAuthorizationPending)
}

func IsSlowDown(err error) bool {
	return hasCode(err, codeSlowDown)
}

func IsAccessDenied(err error) bool {
	return hasCode(err, codeAccessDenied)
}

func IsDeviceCodeExpired(err error) bool {
	return hasCode(err, codeExpiredToken)
}
