package githubactions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	idTokenRequestURLEnvVar   = "ACTIONS_ID_TOKEN_REQUEST_URL"
	idTokenRequestTokenEnvVar = "ACTIONS_ID_TOKEN_REQUEST_TOKEN"
	idTokenResponseLimitBytes = 64 << 10
	idTokenRequestTimeout     = 30 * time.Second
)

var ErrNoIDToken = errors.New("the workflow can't mint an ID token; give the job `permissions: id-token: write` (GitHub mints none for a pull_request run from a fork)")

func IsRunning(getenv func(string) string) bool {
	return getenv("GITHUB_ACTIONS") == "true"
}

func RequestIDToken(ctx context.Context, getenv func(string) string, audience string) (string, error) {
	requestURL, requestToken := getenv(idTokenRequestURLEnvVar), getenv(idTokenRequestTokenEnvVar)
	if requestURL == "" || requestToken == "" {
		return "", ErrNoIDToken
	}
	endpoint, err := url.Parse(requestURL)
	if err != nil {
		return "", fmt.Errorf("%s is not a URL", idTokenRequestURLEnvVar)
	}
	query := endpoint.Query()
	query.Set("audience", audience)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", fmt.Errorf("request an ID token from GitHub Actions: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+requestToken)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: idTokenRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return "", fmt.Errorf("request an ID token from GitHub Actions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the ID token request was refused with HTTP %d", resp.StatusCode)
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, idTokenResponseLimitBytes)).Decode(&body); err != nil {
		return "", fmt.Errorf("read the ID token response: %w", err)
	}
	if body.Value == "" {
		return "", errors.New("the ID token response held no token")
	}
	return body.Value, nil
}
