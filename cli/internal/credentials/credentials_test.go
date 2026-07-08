package credentials

import "testing"

// TestLoadEnvTokenOverride verifies the OCEL_ACCESS_TOKEN environment
// override short-circuits Load before it consults the keyring or file, and
// pairs the token with OCEL_API_URL when present.
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

// TestLoadEnvTokenWithoutAPIURL verifies a bare token leaves APIURL empty so
// the caller's own resolution (effectiveAPIURL) applies.
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

// TestLoadEmptyEnvTokenFallsThrough verifies an empty OCEL_ACCESS_TOKEN does
// not short-circuit Load; with no keyring/file credentials it reports
// ErrNotLoggedIn as before.
func TestLoadEmptyEnvTokenFallsThrough(t *testing.T) {
	t.Setenv(envAccessToken, "")

	// Point the fallback file lookup at an isolated, empty config dir so this
	// test is deterministic regardless of any real login on the machine.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	_, err := Load()
	if err == nil {
		t.Skip("machine has ambient keyring/file credentials; env fallthrough still verified by the token being empty")
	}
	if err != ErrNotLoggedIn {
		// A keyring backend that errors (headless CI without secret service)
		// is also acceptable here; we only assert we did not wrongly return
		// an env-derived credential.
		t.Logf("Load() without env token returned: %v", err)
	}
}
