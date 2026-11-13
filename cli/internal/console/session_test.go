package console

import (
	"testing"
	"time"
)

func TestASessionIsSentOnlyToTheConsoleThatIssuedItAndOnlyUntilItExpires(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	saved := Credentials{AccessToken: "saved-token", APIURL: "https://issuing.example/", Store: FileStore}
	cases := []struct {
		name      string
		env       string
		creds     Credentials
		err       error
		wantURL   string
		wantToken string
	}{
		{name: "the console it is saved for", creds: saved, wantURL: "https://issuing.example", wantToken: "saved-token"},
		{name: "another console", env: "https://other.example", creds: saved, wantURL: "https://other.example"},
		{name: "an expired session", creds: Credentials{AccessToken: "saved-token", APIURL: "https://issuing.example", ExpiresAt: now.Add(-time.Minute), Store: FileStore}, wantURL: "https://issuing.example"},
		{name: "a session that has not expired", creds: Credentials{AccessToken: "saved-token", APIURL: "https://issuing.example", ExpiresAt: now.Add(time.Minute), Store: FileStore}, wantURL: "https://issuing.example", wantToken: "saved-token"},
		{name: "a session from the environment", env: "https://other.example", creds: Credentials{AccessToken: "env-token", Store: EnvironmentStore}, wantURL: "https://other.example", wantToken: "env-token"},
		{name: "nobody logged in", env: "https://other.example", err: ErrNotLoggedIn, wantURL: "https://other.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(URLEnvVar, tc.env)
			baseURL, token := ResolveSession(func() (Credentials, error) { return tc.creds, tc.err }, now)
			if baseURL != tc.wantURL || token != tc.wantToken {
				t.Errorf("ResolveSession() = %q, %q, want %q, %q", baseURL, token, tc.wantURL, tc.wantToken)
			}
		})
	}
}
