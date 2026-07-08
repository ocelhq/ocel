// Package credentials handles secure, persistent storage of the Ocel CLI's
// authentication state on the local machine.
//
// Storage strategy:
//  1. Try the OS keyring (macOS Keychain, Windows Credential Manager, Linux
//     Secret Service / KWallet via D-Bus).
//  2. If no keyring backend is available (common on headless Linux, CI
//     runners, containers), fall back to a JSON file under the user's config
//     directory with 0600 permissions.
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zalando/go-keyring"
)

// service and user identify the secret within the OS keyring. The CLI only
// supports a single signed-in account at a time, so both are fixed.
const (
	service = "ocel-cli"
	user    = "default"
)

// Backend identifies where credentials ended up being stored.
type Backend string

const (
	BackendKeyring Backend = "keyring"
	BackendFile    Backend = "file"
)

// ErrNotLoggedIn is returned by Load when no stored credentials are found.
var ErrNotLoggedIn = errors.New("not logged in")

// Credentials is the persisted authentication state for the CLI.
type Credentials struct {
	// AccessToken is the Better Auth session token obtained via the device
	// authorization flow. Better Auth's device flow does not issue a
	// separate refresh_token: this token is the session itself, and it is
	// long-lived (see the server's session.expiresIn) with rolling renewal
	// on use.
	AccessToken string `json:"access_token"`
	// APIURL is the base URL of the Ocel server this token was issued by.
	APIURL string `json:"api_url"`
	// Email is the signed-in user's email, stored for display purposes only.
	Email string `json:"email,omitempty"`
	// ExpiresAt is the server-reported expiry of the session, stored for
	// local awareness (e.g. to proactively warn the user). The server
	// remains authoritative; a request with an expired token will simply be
	// rejected regardless of what's recorded here.
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// configDir returns the directory Ocel stores its config/fallback
// credentials in, creating it (0700) if necessary.
func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	dir := filepath.Join(base, "ocel")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config directory: %w", err)
	}
	return dir, nil
}

func credentialsFilePath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

// Save persists the credentials, preferring the OS keyring and falling back
// to a permission-restricted file. It reports which backend was used so
// callers can inform the user.
func Save(creds Credentials) (Backend, error) {
	data, err := json.Marshal(creds)
	if err != nil {
		return "", fmt.Errorf("encode credentials: %w", err)
	}

	if err := keyring.Set(service, user, string(data)); err == nil {
		// Clear any stale fallback file from a previous run where the
		// keyring was unavailable, so Load doesn't read outdated data.
		if path, pathErr := credentialsFilePath(); pathErr == nil {
			_ = os.Remove(path)
		}
		return BackendKeyring, nil
	}

	path, err := credentialsFilePath()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write credentials file: %w", err)
	}
	return BackendFile, nil
}

// envAccessToken is the environment variable an out-of-band bearer token can
// be injected through. envAPIURL optionally pairs the origin that token was
// issued by. Together they let non-interactive callers (CI, the e2e harness)
// authenticate the CLI without a device-flow login writing to the keyring or
// credentials file.
const (
	envAccessToken = "OCEL_ACCESS_TOKEN"
	envAPIURL      = "OCEL_API_URL"
)

// Load reads previously stored credentials. It first honors an
// OCEL_ACCESS_TOKEN environment override (taking precedence over any stored
// credentials, so a headless run can authenticate without touching the
// keyring/file), then checks the OS keyring, then falls back to the local
// file. It returns ErrNotLoggedIn if none of these has anything.
func Load() (Credentials, error) {
	var creds Credentials

	if token := os.Getenv(envAccessToken); token != "" {
		// A bare token with no OCEL_API_URL leaves APIURL empty on purpose,
		// so effectiveAPIURL falls through to its own resolution ($OCEL_API_URL,
		// then the default origin).
		return Credentials{
			AccessToken: token,
			APIURL:      os.Getenv(envAPIURL),
		}, nil
	}

	if secret, err := keyring.Get(service, user); err == nil {
		if err := json.Unmarshal([]byte(secret), &creds); err != nil {
			return Credentials{}, fmt.Errorf("decode stored credentials: %w", err)
		}
		return creds, nil
	}

	path, err := credentialsFilePath()
	if err != nil {
		return Credentials{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Credentials{}, ErrNotLoggedIn
		}
		return Credentials{}, fmt.Errorf("read credentials file: %w", err)
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return Credentials{}, fmt.Errorf("decode stored credentials: %w", err)
	}
	return creds, nil
}

// Delete removes any stored credentials from both the keyring and the
// fallback file. It does not error if nothing was stored.
func Delete() error {
	if err := keyring.Delete(service, user); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("remove credentials from keyring: %w", err)
	}

	path, err := credentialsFilePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove credentials file: %w", err)
	}
	return nil
}
