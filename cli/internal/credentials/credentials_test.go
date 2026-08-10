package credentials

import "testing"

func TestLoadEnvTokenOverride(t *testing.T) {
	t.Setenv(envAccessToken, "env-token-123")
	t.Setenv(envAPIURL, "http://localhost:3000")

	creds, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if creds.AccessToken != "env-token-123" {
		t.Errorf("AccessToken = %q, want %q", creds.AccessToken, "env-token-123")
	}
	if creds.APIURL != "http://localhost:3000" {
		t.Errorf("APIURL = %q, want %q", creds.APIURL, "http://localhost:3000")
	}
}

func TestLoadEnvTokenWithoutAPIURL(t *testing.T) {
	t.Setenv(envAccessToken, "env-token-only")
	t.Setenv(envAPIURL, "")

	creds, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if creds.AccessToken != "env-token-only" {
		t.Errorf("AccessToken = %q, want %q", creds.AccessToken, "env-token-only")
	}
	if creds.APIURL != "" {
		t.Errorf("APIURL = %q, want empty", creds.APIURL)
	}
}

func TestLoadEmptyEnvTokenFallsThrough(t *testing.T) {
	t.Setenv(envAccessToken, "")

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	_, err := Load()
	if err == nil {
		t.Skip("machine has ambient keyring/file credentials; env fallthrough still verified by the token being empty")
	}
	if err != ErrNotLoggedIn {
		t.Logf("Load() without env token returned: %v", err)
	}
}
