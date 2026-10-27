package deployreport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeIDTokenIssuer struct {
	mu        sync.Mutex
	audiences []string
	auth      []string
	status    int
	body      string
}

func serveIDTokens(t *testing.T, fake *fakeIDTokenIssuer) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.audiences = append(fake.audiences, r.URL.Query().Get("audience"))
		fake.auth = append(fake.auth, r.Header.Get("Authorization"))
		issued := len(fake.audiences)
		fake.mu.Unlock()
		switch {
		case r.URL.Query().Get("api-version") != "2.0":
			http.Error(w, "the request URL lost its query", http.StatusBadRequest)
		case fake.status != 0:
			http.Error(w, http.StatusText(fake.status), fake.status)
		case fake.body != "":
			fmt.Fprint(w, fake.body)
		default:
			fmt.Fprintf(w, `{"count":1,"value":"id-token-%d"}`, issued)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/idtoken?api-version=2.0"
}

func idTokenEnv(requestURL string) func(string) string {
	env := map[string]string{
		"ACTIONS_ID_TOKEN_REQUEST_URL":   requestURL,
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "request-token",
	}
	return func(name string) string { return env[name] }
}

func TestAnIDTokenIsRequestedForTheAudienceWithTheWorkflowsRequestToken(t *testing.T) {
	t.Parallel()
	issuer := &fakeIDTokenIssuer{}

	token, err := requestIDToken(context.Background(), idTokenEnv(serveIDTokens(t, issuer)), "https://console.example.com")

	if err != nil || token != "id-token-1" {
		t.Fatalf("requestIDToken() = %q, %v, want the issued token", token, err)
	}
	if issuer.audiences[0] != "https://console.example.com" || issuer.auth[0] != "Bearer request-token" {
		t.Errorf("audience %q, Authorization %q, want the console origin and the workflow's request token", issuer.audiences[0], issuer.auth[0])
	}
}

func TestAJobThatCannotRequestAnIDTokenIsToldToGrantIDTokenWriteAndAboutForks(t *testing.T) {
	t.Parallel()
	for _, missing := range []string{"ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN"} {
		t.Run(missing, func(t *testing.T) {
			t.Parallel()
			issuer := &fakeIDTokenIssuer{}
			env := idTokenEnv(serveIDTokens(t, issuer))
			getenv := func(name string) string {
				if name == missing {
					return ""
				}
				return env(name)
			}

			_, err := requestIDToken(context.Background(), getenv, "https://console.example.com")

			if !errors.Is(err, errNoIDToken) || !strings.Contains(err.Error(), "permissions: id-token: write") || !strings.Contains(err.Error(), "fork") || len(issuer.audiences) != 0 {
				t.Errorf("requestIDToken() error = %v after %d requests, want no request and the fix naming `permissions: id-token: write` and forks", err, len(issuer.audiences))
			}
		})
	}
}

func TestAnIDTokenRequestThatGitHubRefusesNamesTheStatusAndNotTheURL(t *testing.T) {
	t.Parallel()
	requestURL := serveIDTokens(t, &fakeIDTokenIssuer{status: http.StatusForbidden})

	_, err := requestIDToken(context.Background(), idTokenEnv(requestURL), "https://console.example.com")

	if err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), strings.TrimSuffix(requestURL, "/idtoken?api-version=2.0")) {
		t.Errorf("requestIDToken() error = %v, want the 403 without the request URL", err)
	}
}

func TestAnIDTokenResponseWithoutATokenIsAnError(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{"empty value": `{"value":""}`, "not JSON": "<html>"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requestURL := serveIDTokens(t, &fakeIDTokenIssuer{body: body})

			token, err := requestIDToken(context.Background(), idTokenEnv(requestURL), "https://console.example.com")

			if err == nil || token != "" {
				t.Errorf("requestIDToken() = %q, %v, want an error and no token", token, err)
			}
		})
	}
}

func TestTheAudienceIsTheConsolesCanonicalOrigin(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://Console.Example.COM/app/": "https://console.example.com",
		"https://console.example.com:443":  "https://console.example.com",
		"http://console.example.com:80/":   "http://console.example.com",
		"http://localhost:8080/console":    "http://localhost:8080",
		"https://console.example.com:80":   "https://console.example.com:80",
	}
	for apiURL, want := range cases {
		if got, err := readAudience(apiURL); err != nil || got != want {
			t.Errorf("readAudience(%q) = %q, %v, want %q", apiURL, got, err, want)
		}
	}
	for _, malformed := range []string{"console.example.com", "https://", "http://[::1"} {
		if got, err := readAudience(malformed); err == nil {
			t.Errorf("readAudience(%q) = %q, want an error", malformed, got)
		}
	}
}
