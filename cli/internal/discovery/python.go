package discovery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/pkg/statedir"
)

const (
	pythonEntryFile = statedir.Name + "/discovery.py"
	pythonOnPath    = "python3"
)

var pythonProjectFiles = []string{"pyproject.toml", "requirements.txt"}

var pythonInterpreters = []string{".venv/bin/python", "venv/bin/python"}

func pythonCommand(ctx context.Context, configDir string, root Root, server Server) (*exec.Cmd, error) {
	runRoot, err := pythonRunRoot(configDir, root.Dir)
	if err != nil {
		return nil, err
	}
	pkg, err := pythonPackage(runRoot, root.Dir)
	if err != nil {
		return nil, err
	}

	entry := filepath.Join(runRoot, filepath.FromSlash(pythonEntryFile))
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	source := fmt.Sprintf(
		"import sys\nfrom importlib import import_module\n\nsys.path.insert(0, %s)\nimport_module(%s)\n",
		strconv.Quote(runRoot), strconv.Quote(pkg),
	)
	if err := os.WriteFile(entry, []byte(source), 0o644); err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}

	cmd := exec.CommandContext(ctx, PythonInterpreter(runRoot), "./"+pythonEntryFile)
	cmd.Dir = runRoot
	cmd.Env = append(append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1"), server.Env()...)
	return cmd, nil
}

func pythonRunRoot(configDir, dir string) (string, error) {
	runRoot, _, err := walkUp(configDir, dir, hasAPythonProject)
	return runRoot, err
}

type PythonPackage struct {
	Name string
	Dir  string
}

func ListPythonPackages(configDir string, roots []Root, dir string) ([]PythonPackage, error) {
	runRoot, err := pythonRunRoot(configDir, dir)
	if err != nil {
		return nil, err
	}
	var packages []PythonPackage
	for _, root := range roots {
		if root.Language != language.Python {
			continue
		}
		rootRunRoot, inAProject, err := walkUp(configDir, root.Dir, hasAPythonProject)
		if err != nil {
			return nil, err
		}
		if rootRunRoot != runRoot && inAProject {
			continue
		}
		name, err := pythonPackage(rootRunRoot, root.Dir)
		if err != nil {
			return nil, err
		}
		if at := slices.IndexFunc(packages, func(p PythonPackage) bool { return p.Name == name }); at >= 0 {
			return nil, fmt.Errorf("discovery: %s and %s are both imported as %q: rename one", packages[at].Dir, root.Dir, name)
		}
		packages = append(packages, PythonPackage{Name: name, Dir: root.Dir})
	}
	return packages, nil
}

func hasAPythonProject(at string) bool {
	for _, name := range pythonProjectFiles {
		if info, err := os.Stat(filepath.Join(at, name)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func pythonPackage(runRoot, dir string) (string, error) {
	rel, err := filepath.Rel(runRoot, dir)
	if err != nil {
		return "", fmt.Errorf("discovery: %s is not inside the project at %s", dir, runRoot)
	}
	if rel == "." {
		return "", fmt.Errorf("discovery: %s holds the python project file, so no import names it: move the declarations into a package beside the project file", dir)
	}
	segments := strings.Split(filepath.ToSlash(rel), "/")
	if len(segments) != 1 {
		return "", fmt.Errorf("discovery: %s is a python package %s imports as %q, and a discovery folder is a package beside the project file, not under one", dir, runRoot, strings.Join(segments, "."))
	}
	return segments[0], nil
}

func PythonInterpreter(runRoot string) string {
	for _, candidate := range pythonInterpreters {
		path := filepath.Join(runRoot, filepath.FromSlash(candidate))
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return pythonOnPath
}
