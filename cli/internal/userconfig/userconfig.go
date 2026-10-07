package userconfig

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
)

const (
	dirName          = "ocel"
	settingsFileName = "settings.json"
	lockFileName     = "settings.lock"
	installIDKey     = "install_id"
	deployedKey      = "deployed"

	liveHashKeySetting = "live_hash_key"
	liveHashKeyBytes   = 32
)

type Settings map[string]json.RawMessage

func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(base, dirName), nil
}

func EnsureDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config directory: %w", err)
	}
	return dir, nil
}

func Read() Settings {
	dir, err := Dir()
	if err != nil {
		return Settings{}
	}
	lock := flock.New(filepath.Join(dir, lockFileName), flock.SetFlag(os.O_RDONLY))
	if err := lock.RLock(); err == nil {
		defer func() { _ = lock.Unlock() }()
	}
	return readSettings(filepath.Join(dir, settingsFileName))
}

func Update(change func(Settings)) error {
	dir, err := EnsureDir()
	if err != nil {
		return err
	}
	lock := flock.New(filepath.Join(dir, lockFileName))
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("lock settings: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	path := filepath.Join(dir, settingsFileName)
	settings := readSettings(path)
	change(settings)
	return writeSettings(path, settings)
}

func EnsureInstallID() (string, error) {
	if id, ok := storedInstallID(Read()); ok {
		return id, nil
	}
	var id string
	err := Update(func(s Settings) {
		if stored, ok := storedInstallID(s); ok {
			id = stored
			return
		}
		id = uuid.NewString()
		s[installIDKey], _ = json.Marshal(id)
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func EnsureLiveHashKey() ([]byte, error) {
	if key, ok := readLiveHashKey(Read()); ok {
		return key, nil
	}
	var key []byte
	err := Update(func(s Settings) {
		if stored, ok := readLiveHashKey(s); ok {
			key = stored
			return
		}
		key = make([]byte, liveHashKeyBytes)
		_, _ = rand.Read(key)
		s[liveHashKeySetting], _ = json.Marshal(base64.StdEncoding.EncodeToString(key))
	})
	if err != nil {
		return nil, err
	}
	return key, nil
}

func readLiveHashKey(s Settings) ([]byte, bool) {
	var encoded string
	if err := json.Unmarshal(s[liveHashKeySetting], &encoded); err != nil {
		return nil, false
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != liveHashKeyBytes {
		return nil, false
	}
	return key, true
}

func HasDeployed() bool {
	var deployed bool
	return json.Unmarshal(Read()[deployedKey], &deployed) == nil && deployed
}

func MarkDeployed() error {
	return Update(func(s Settings) { s[deployedKey] = json.RawMessage("true") })
}

func storedInstallID(s Settings) (string, bool) {
	var id string
	if err := json.Unmarshal(s[installIDKey], &id); err != nil {
		return "", false
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", false
	}
	return id, true
}

func readSettings(path string) Settings {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Settings{}
	}
	var settings Settings
	if err := json.Unmarshal(raw, &settings); err != nil || settings == nil {
		return Settings{}
	}
	return settings
}

func writeSettings(path string, settings Settings) error {
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	dir := filepath.Dir(path)
	staged, err := os.CreateTemp(dir, "."+settingsFileName+".*")
	if err != nil {
		return fmt.Errorf("create staged settings file: %w", err)
	}
	defer os.Remove(staged.Name())

	if _, err := staged.Write(append(raw, '\n')); err != nil {
		staged.Close()
		return fmt.Errorf("write settings: %w", err)
	}
	if err := staged.Sync(); err != nil {
		staged.Close()
		return fmt.Errorf("sync settings: %w", err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	if err := os.Rename(staged.Name(), path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	syncDir(dir)
	return nil
}

func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	handle, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = handle.Sync()
	_ = handle.Close()
}
