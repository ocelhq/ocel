package buildoutput

import (
	"fmt"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/statedir"
)

const (
	Dir = statedir.Name + "/output"

	appsDir = "apps"

	FunctionsDir      = "functions"
	FunctionDirSuffix = ".func"
	RootFunction      = "/"
	RootFunctionDir   = "index" + FunctionDirSuffix
)

func Root(projectDir string) (string, error) {
	if !filepath.IsAbs(projectDir) {
		return "", fmt.Errorf("the build output of project directory %q: the directory is not absolute, so the output would land wherever the process runs", projectDir)
	}
	return filepath.Join(projectDir, filepath.FromSlash(Dir)), nil
}

func AppsRoot(root string) string { return filepath.Join(root, appsDir) }

func AppRoot(root, app string) string { return filepath.Join(AppsRoot(root), app) }
