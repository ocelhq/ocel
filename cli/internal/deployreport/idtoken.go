package deployreport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	idTokenRequestURLEnvVar   = "ACTIONS_ID_TOKEN_REQUEST_URL"
	idTokenRequestTokenEnvVar = "ACTIONS_ID_TOKEN_REQUEST_TOKEN"
	idTokenResponseLimitBytes = 64 << 10
	idTokenRequestTimeout     = 30 * time.Second
)

var errNoIDToken = errors.New("the workflow can't mint an ID token; give the job `permissions: id-token: write` (GitHub mints none for a pull_request run from a fork)")

var defaultPorts = map[string]string{"http": "80", "https": "443"}

func readAudience(apiURL string) (string, error) {
	parsed, err := url.Parse(apiURL)
	if err != nil || defaultPorts[parsed.Scheme] == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("the console URL %q is not an http or https URL", apiURL)
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" && port != defaultPorts[parsed.Scheme] {
		host += ":" + port
	}
	return parsed.Scheme + "://" + host, nil
}

func requestIDToken(ctx context.Context, getenv func(string) string, audience string) (string, error) {
	requestURL, requestToken := getenv(idTokenRequestURLEnvVar), getenv(idTokenRequestTokenEnvVar)
	if requestURL == "" || requestToken == "" {
		return "", errNoIDToken
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
