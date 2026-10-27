package githubactionstest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type IDTokens struct {
	Status int
	Body   string

	mu        sync.Mutex
	audiences []string
	auth      []string
}

func ServeIDTokens(t *testing.T, tokens *IDTokens) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens.mu.Lock()
		tokens.audiences = append(tokens.audiences, r.URL.Query().Get("audience"))
		tokens.auth = append(tokens.auth, r.Header.Get("Authorization"))
		issued := len(tokens.audiences)
		tokens.mu.Unlock()
		switch {
		case r.URL.Query().Get("api-version") != "2.0":
			http.Error(w, "the request URL lost its query", http.StatusBadRequest)
		case tokens.Status != 0:
			http.Error(w, http.StatusText(tokens.Status), tokens.Status)
		case tokens.Body != "":
			fmt.Fprint(w, tokens.Body)
		default:
			fmt.Fprintf(w, `{"count":1,"value":"id-token-%d"}`, issued)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/idtoken?api-version=2.0"
}

func (i *IDTokens) Audiences() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.audiences...)
}

func (i *IDTokens) Authorizations() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.auth...)
}

func Env(requestURL string) map[string]string {
	return map[string]string{
		"GITHUB_ACTIONS":                 "true",
		"ACTIONS_ID_TOKEN_REQUEST_URL":   requestURL,
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "request-token",
	}
}
