package auth

import (
	"context"
	"errors"
)

const (
	ErrCodeAuthorizationPending = "authorization_pending"
	ErrCodeSlowDown             = "slow_down"
	ErrCodeAccessDenied         = "access_denied"
	ErrCodeExpiredToken         = "expired_token"
	ErrCodeInvalidGrant         = "invalid_grant"
	ErrCodeInvalidRequest       = "invalid_request"
	ErrCodeInvalidClient        = "invalid_client"
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
	body := map[string]string{"client_id": ClientID}
	if err := c.postJSON(ctx, "/api/auth/device/code", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type TokenResult struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

func (c *Client) PollToken(ctx context.Context, deviceCode string) (*TokenResult, error) {
	var out TokenResult
	body := map[string]string{
		"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
		"device_code": deviceCode,
		"client_id":   ClientID,
	}
	if err := c.postJSON(ctx, "/api/auth/device/token", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func IsPending(err error) bool {
	return hasCode(err, ErrCodeAuthorizationPending)
}

func IsSlowDown(err error) bool {
	return hasCode(err, ErrCodeSlowDown)
}

func IsAccessDenied(err error) bool {
	return hasCode(err, ErrCodeAccessDenied)
}

func IsExpired(err error) bool {
	return hasCode(err, ErrCodeExpiredToken)
}

func hasCode(err error, code string) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == code
	}
	return false
}
