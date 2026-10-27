package githubactions_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/githubactions"
	"github.com/ocelhq/ocel/cli/internal/githubactions/githubactionstest"
)

func idTokenEnv(requestURL string) func(string) string {
	env := githubactionstest.Env(requestURL)
	return func(name string) string { return env[name] }
}

func TestAnIDTokenIsRequestedForTheAudienceWithTheWorkflowsRequestToken(t *testing.T) {
	t.Parallel()
	issuer := &githubactionstest.IDTokens{}

	token, err := githubactions.RequestIDToken(context.Background(), idTokenEnv(githubactionstest.ServeIDTokens(t, issuer)), "https://console.example.com")

	if err != nil || token != "id-token-1" {
		t.Fatalf("githubactions.RequestIDToken() = %q, %v, want the issued token", token, err)
	}
	if issuer.Audiences()[0] != "https://console.example.com" || issuer.Authorizations()[0] != "Bearer request-token" {
		t.Errorf("audience %q, Authorization %q, want the console origin and the workflow's request token", issuer.Audiences()[0], issuer.Authorizations()[0])
	}
}

func TestAJobThatCannotRequestAnIDTokenIsToldToGrantIDTokenWriteAndAboutForks(t *testing.T) {
	t.Parallel()
	for _, missing := range []string{"ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN"} {
		t.Run(missing, func(t *testing.T) {
			t.Parallel()
			issuer := &githubactionstest.IDTokens{}
			env := idTokenEnv(githubactionstest.ServeIDTokens(t, issuer))
			getenv := func(name string) string {
				if name == missing {
					return ""
				}
				return env(name)
			}

			_, err := githubactions.RequestIDToken(context.Background(), getenv, "https://console.example.com")

			if !errors.Is(err, githubactions.ErrNoIDToken) || !strings.Contains(err.Error(), "permissions: id-token: write") || !strings.Contains(err.Error(), "fork") || len(issuer.Audiences()) != 0 {
				t.Errorf("githubactions.RequestIDToken() error = %v after %d requests, want no request and the fix naming `permissions: id-token: write` and forks", err, len(issuer.Audiences()))
			}
		})
	}
}

func TestAnIDTokenRequestThatGitHubRefusesNamesTheStatusAndNotTheURL(t *testing.T) {
	t.Parallel()
	requestURL := githubactionstest.ServeIDTokens(t, &githubactionstest.IDTokens{Status: http.StatusForbidden})

	_, err := githubactions.RequestIDToken(context.Background(), idTokenEnv(requestURL), "https://console.example.com")

	if err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), strings.TrimSuffix(requestURL, "/idtoken?api-version=2.0")) {
		t.Errorf("githubactions.RequestIDToken() error = %v, want the 403 without the request URL", err)
	}
}

func TestAnIDTokenResponseWithoutATokenIsAnError(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{"empty value": `{"value":""}`, "not JSON": "<html>"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requestURL := githubactionstest.ServeIDTokens(t, &githubactionstest.IDTokens{Body: body})

			token, err := githubactions.RequestIDToken(context.Background(), idTokenEnv(requestURL), "https://console.example.com")

			if err == nil || token != "" {
				t.Errorf("githubactions.RequestIDToken() = %q, %v, want an error and no token", token, err)
			}
		})
	}
}
