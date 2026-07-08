package authclient

import (
	"context"
	"errors"
)

// Device authorization error codes, as defined by RFC 8628 and returned by
// Better Auth's /device/token endpoint. Compare against these using
// errors.Is on the returned *APIError, or use the IsXxx helpers below.
const (
	ErrCodeAuthorizationPending = "authorization_pending"
	ErrCodeSlowDown             = "slow_down"
	ErrCodeAccessDenied         = "access_denied"
	ErrCodeExpiredToken         = "expired_token"
	ErrCodeInvalidGrant         = "invalid_grant"
	ErrCodeInvalidRequest       = "invalid_request"
	ErrCodeInvalidClient        = "invalid_client"
)

// DeviceCode is the response from requesting device authorization.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// RequestDeviceCode initiates the device authorization grant.
func (c *Client) RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	var out DeviceCode
	body := map[string]string{"client_id": ClientID}
	if err := c.postJSON(ctx, "/api/auth/device/code", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TokenResult is the response once the user has approved the device.
type TokenResult struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

// PollToken makes a single attempt to exchange the device code for an
// access token. Callers are expected to call this in a loop, sleeping
// between attempts according to the interval returned by
// RequestDeviceCode (and any slow_down responses), stopping on any
// terminal error (see IsPending/IsSlowDown below to distinguish
// "keep polling" from "stop").
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

// IsPending reports whether err indicates the user hasn't approved (or
// denied) the request yet, i.e. polling should continue unchanged.
func IsPending(err error) bool {
	return hasCode(err, ErrCodeAuthorizationPending)
}

// IsSlowDown reports whether err indicates the CLI should increase its
// polling interval and keep going.
func IsSlowDown(err error) bool {
	return hasCode(err, ErrCodeSlowDown)
}

// IsAccessDenied reports whether the user explicitly denied the request.
func IsAccessDenied(err error) bool {
	return hasCode(err, ErrCodeAccessDenied)
}

// IsExpired reports whether the device code expired before it was approved.
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
