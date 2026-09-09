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

func (l Lock) Bytes() []byte {
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return nil
	}
	return append(raw, '\n')
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
	if err := os.WriteFile(Path(dir), lock.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", Name, err)
	}
	return nil
}
