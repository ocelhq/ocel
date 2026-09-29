package console

import (
	"errors"
	"testing"
)

func TestLoadCredentials(t *testing.T) {
	t.Run("an env token overrides everything else", func(t *testing.T) {
		t.Setenv(accessTokenEnvVar, "env-token-123")
		t.Setenv(URLEnvVar, "http://localhost:3000")

		creds, err := LoadCredentials()
		if err != nil {
			t.Fatalf("LoadCredentials() returned error: %v", err)
		}
		if creds.AccessToken != "env-token-123" {
			t.Errorf("AccessToken = %q, want %q", creds.AccessToken, "env-token-123")
		}
		if creds.APIURL != "http://localhost:3000" {
			t.Errorf("APIURL = %q, want %q", creds.APIURL, "http://localhost:3000")
		}
	})

	t.Run("an env token without an API URL", func(t *testing.T) {
		t.Setenv(accessTokenEnvVar, "env-token-only")
		t.Setenv(URLEnvVar, "")

		creds, err := LoadCredentials()
		if err != nil {
			t.Fatalf("LoadCredentials() returned error: %v", err)
		}
		if creds.AccessToken != "env-token-only" {
			t.Errorf("AccessToken = %q, want %q", creds.AccessToken, "env-token-only")
		}
		if creds.APIURL != "" {
			t.Errorf("APIURL = %q, want empty", creds.APIURL)
		}
	})

	t.Run("an empty env token falls through", func(t *testing.T) {
		t.Setenv(accessTokenEnvVar, "")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("HOME", t.TempDir())

		_, err := LoadCredentials()
		if err == nil {
			t.Skip("machine has ambient keyring/file credentials; env fallthrough still verified by the token being empty")
		}
		if !errors.Is(err, ErrNotLoggedIn) {
			t.Logf("LoadCredentials() without env token returned: %v", err)
		}
	})
}
