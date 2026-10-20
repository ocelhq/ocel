package sourcearchive

import (
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/ocelhq/ocel/cli/internal/workspace"
)

const (
	gitEntry      = ".git"
	cargoManifest = "Cargo.toml"
)

func Root(projectDir string) (string, error) {
	dir, err := filepath.Abs(projectDir)
	if err != nil {
		return "", err
	}
	root := dir
	widen := func(found string) {
		if found != "" && len(found) < len(root) {
			root = found
		}
	}
	widen(gitRoot(dir))
	if js, ok := workspace.Enclosing(dir); ok {
		widen(js)
	}
	widen(cargoWorkspaceRoot(dir))
	return root, nil
}

func gitRoot(dir string) string {
	for at := dir; ; at = filepath.Dir(at) {
		if _, err := os.Lstat(filepath.Join(at, gitEntry)); err == nil {
			return at
		}
		if filepath.Dir(at) == at {
			return ""
		}
	}
}

func cargoWorkspaceRoot(dir string) string {
	found := ""
	for at := dir; ; at = filepath.Dir(at) {
		if declaresCargoWorkspace(filepath.Join(at, cargoManifest)) {
			found = at
		}
		if filepath.Dir(at) == at {
			return found
		}
	}
}

func declaresCargoWorkspace(manifest string) bool {
	read, err := os.ReadFile(manifest)
	if err != nil {
		return false
	}
	var tables map[string]any
	if toml.Unmarshal(read, &tables) != nil {
		return false
	}
	_, declares := tables["workspace"]
	return declares
}
