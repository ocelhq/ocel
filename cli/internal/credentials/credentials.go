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

const (
	service = "ocel-cli"
	user    = "default"
)

type Backend string

const (
	BackendKeyring Backend = "keyring"
	BackendFile    Backend = "file"
)

var ErrNotLoggedIn = errors.New("not logged in")

type Credentials struct {
	AccessToken string    `json:"access_token"`
	APIURL      string    `json:"api_url"`
	Email       string    `json:"email,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
}

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

func Save(creds Credentials) (Backend, error) {
	data, err := json.Marshal(creds)
	if err != nil {
		return "", fmt.Errorf("encode credentials: %w", err)
	}

	if err := keyring.Set(service, user, string(data)); err == nil {
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

const (
	envAccessToken = "OCEL_ACCESS_TOKEN"
	envAPIURL      = "OCEL_API_URL"
)

func Load() (Credentials, error) {
	var creds Credentials

	if token := os.Getenv(envAccessToken); token != "" {
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
