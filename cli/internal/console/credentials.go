package console

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	keyringService      = "ocel-cli"
	keyringUser         = "default"
	credentialsFileName = "credentials.json"
	accessTokenEnvVar   = "OCEL_ACCESS_TOKEN"
)

type CredentialStore string

const (
	KeyringStore CredentialStore = "keyring"
	FileStore    CredentialStore = "file"
)

var ErrNotLoggedIn = errors.New("not logged in")

type Credentials struct {
	AccessToken string    `json:"access_token"`
	APIURL      string    `json:"api_url"`
	Email       string    `json:"email,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
}

func ensureConfigDir() (string, error) {
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

func ensureCredentialsFilePath() (string, error) {
	dir, err := ensureConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, credentialsFileName), nil
}

func SaveCredentials(creds Credentials) (CredentialStore, error) {
	data, err := json.Marshal(creds)
	if err != nil {
		return "", fmt.Errorf("encode credentials: %w", err)
	}

	if err := keyring.Set(keyringService, keyringUser, string(data)); err == nil {
		if path, pathErr := ensureCredentialsFilePath(); pathErr == nil {
			_ = os.Remove(path)
		}
		return KeyringStore, nil
	}

	path, err := ensureCredentialsFilePath()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write credentials file: %w", err)
	}
	return FileStore, nil
}

func LoadCredentials() (Credentials, error) {
	var creds Credentials

	if token := os.Getenv(accessTokenEnvVar); token != "" {
		return Credentials{
			AccessToken: token,
			APIURL:      os.Getenv(URLEnvVar),
		}, nil
	}

	if secret, err := keyring.Get(keyringService, keyringUser); err == nil {
		if err := json.Unmarshal([]byte(secret), &creds); err != nil {
			return Credentials{}, fmt.Errorf("decode stored credentials: %w", err)
		}
		return creds, nil
	}

	path, err := ensureCredentialsFilePath()
	if err != nil {
		return Credentials{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Credentials{}, ErrNotLoggedIn
		}
		return Credentials{}, fmt.Errorf("read credentials file: %w", err)
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return Credentials{}, fmt.Errorf("decode stored credentials: %w", err)
	}
	return creds, nil
}

func DeleteCredentials() error {
	if err := keyring.Delete(keyringService, keyringUser); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("remove credentials from keyring: %w", err)
	}

	path, err := ensureCredentialsFilePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove credentials file: %w", err)
	}
	return nil
}
