package buildoutput

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/statedir"
)

const (
	Dir = statedir.Name + "/output"

	appsDir = "apps"
)

func Root(projectDir string) (string, error) {
	if !filepath.IsAbs(projectDir) {
		return "", fmt.Errorf("the build output of project directory %q: the directory is not absolute, so the output would land wherever the process runs", projectDir)
	}
	return filepath.Join(projectDir, filepath.FromSlash(Dir)), nil
}

func AppsRoot(root string) string { return filepath.Join(root, appsDir) }

func AppRoot(root, app string) string { return filepath.Join(AppsRoot(root), app) }

func ReadServeDescriptor(root, app string) (edge.ServeDescriptor, bool, error) {
	raw, err := os.ReadFile(filepath.Join(AppRoot(root, app), edge.ServeDescriptorFile))
	if errors.Is(err, fs.ErrNotExist) {
		return edge.ServeDescriptor{}, false, nil
	}
	if err != nil {
		return edge.ServeDescriptor{}, false, fmt.Errorf("read serve descriptor for %s: %w", app, err)
	}
	var desc edge.ServeDescriptor
	if err := json.Unmarshal(raw, &desc); err != nil {
		return edge.ServeDescriptor{}, false, fmt.Errorf("parse serve descriptor for %s: %w", app, err)
	}
	return desc, true, nil
}
