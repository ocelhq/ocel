package lockfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/providers"
)

const Name = "ocel.lock"

type Lock struct {
	CLI       string                       `json:"cli"`
	Providers map[string]map[string]string `json:"providers"`
}

func FromChecksums(version string, sums map[string]string) Lock {
	lock := Lock{CLI: version, Providers: map[string]map[string]string{}}
	for asset, digest := range sums {
		parsed, ok := providers.ParseAssetName(asset)
		if !ok || parsed.Version != version {
			continue
		}
		platform := providers.Platform{GOOS: parsed.GOOS, GOARCH: parsed.GOARCH}
		pinned, held := lock.Providers[parsed.Name]
		if !held {
			pinned = map[string]string{}
			lock.Providers[parsed.Name] = pinned
		}
		pinned[platform.Dir()] = digest
	}
	return lock
}

func (l Lock) Digest(name, platform string) (string, bool) {
	digest, held := l.Providers[name][platform]
	return digest, held
}

func (l Lock) Bytes() ([]byte, error) {
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", Name, err)
	}
	return append(raw, '\n'), nil
}

func Path(dir string) string { return filepath.Join(dir, Name) }

func Read(dir string) (Lock, bool, error) {
	raw, err := os.ReadFile(Path(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return Lock{}, false, nil
	}
	if err != nil {
		return Lock{}, false, fmt.Errorf("read %s: %w", Name, err)
	}
	var lock Lock
	if err := json.Unmarshal(raw, &lock); err != nil {
		return Lock{}, false, fmt.Errorf("%s is not readable as a lock: %w", Name, err)
	}
	return lock, true, nil
}

func Write(dir string, lock Lock) error {
	raw, err := lock.Bytes()
	if err != nil {
		return err
	}

	staged, err := os.CreateTemp(dir, "."+Name+".*")
	if err != nil {
		return fmt.Errorf("open a file to write %s through: %w", Name, err)
	}
	defer os.Remove(staged.Name())

	if _, err := staged.Write(raw); err != nil {
		staged.Close()
		return fmt.Errorf("write %s: %w", Name, err)
	}
	if err := staged.Chmod(0o644); err != nil {
		staged.Close()
		return fmt.Errorf("write %s: %w", Name, err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("write %s: %w", Name, err)
	}
	if err := os.Rename(staged.Name(), Path(dir)); err != nil {
		return fmt.Errorf("write %s: %w", Name, err)
	}
	return nil
}
