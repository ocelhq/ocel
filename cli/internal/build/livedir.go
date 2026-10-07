package build

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/processenv"
)

func refuseLiveKeysOutsideTheDir(live map[string]string) error {
	for key := range live {
		if !filepath.IsLocal(key) || filepath.Base(key) != key {
			return fmt.Errorf("a variable is declared as %q, which is no file name a build can read it from; rename it where it is declared", key)
		}
	}
	return nil
}

func writeLiveDir(live map[string]string) (dir string, err error) {
	dir, err = os.MkdirTemp("", "ocel-live-")
	if err != nil {
		return "", fmt.Errorf("create the build's live dir: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	for key, value := range live {
		if err := os.WriteFile(filepath.Join(dir, key), []byte(value), 0o600); err != nil {
			return "", fmt.Errorf("write %s into the build's live dir: %w", key, err)
		}
	}
	return dir, nil
}

func withLiveDir(env map[string]string, dir string) map[string]string {
	out := make(map[string]string, len(env)+1)
	maps.Copy(out, env)
	out[processenv.LiveDirEnvVar] = dir
	return out
}
