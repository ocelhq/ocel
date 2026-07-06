// Package projectconfig locates, transpiles, and executes a user's
// ocel.config.ts to resolve their project's configuration.
package projectconfig

import (
	"os"
	"path/filepath"
)

// ConfigFileName is the name of the file Resolve looks for.
const ConfigFileName = "ocel.config.ts"

// defaultDiscoveryPath is used for discovery.paths when the config omits it.
var defaultDiscoveryPaths = []string{"ocel"}

// Discovery controls where the CLI looks for resource declarations.
type Discovery struct {
	Paths []string
}

// Config is the resolved, defaulted project configuration read from
// ocel.config.ts.
type Config struct {
	ProjectID string
	Discovery Discovery
}

// findConfigFile walks up from startDir (tsconfig-style) looking for the
// nearest ancestor ConfigFileName. It returns os.ErrNotExist if none is
// found by the time it reaches the filesystem root.
func findConfigFile(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for {
		candidate := filepath.Join(dir, ConfigFileName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
