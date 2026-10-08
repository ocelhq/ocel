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

func ReadHosting(root, app string) (edge.Hosting, bool, error) {
	raw, err := os.ReadFile(filepath.Join(AppRoot(root, app), edge.HostingFile))
	if errors.Is(err, fs.ErrNotExist) {
		return edge.Hosting{}, false, nil
	}
	if err != nil {
		return edge.Hosting{}, false, fmt.Errorf("read hosting.json of %s: %w", app, err)
	}
	var hosting edge.Hosting
	if err := json.Unmarshal(raw, &hosting); err != nil {
		return edge.Hosting{}, false, fmt.Errorf("parse hosting.json of %s: %w", app, err)
	}
	return hosting, true, nil
}
