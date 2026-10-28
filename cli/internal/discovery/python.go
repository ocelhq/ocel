package discovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/workdir"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/tracing"
)

const pythonOnPath = "python3"

var pythonProjectFiles = []string{"pyproject.toml", "requirements.txt"}

var pythonInterpreters = []string{".venv/bin/python", "venv/bin/python"}

func pythonCommands(ctx context.Context, configDir string, roots []Root, server Server) ([]*tracing.Cmd, error) {
	var runRoots []string
	imports := map[string][]string{}
	for _, root := range roots {
		runRoot, err := pythonRunRoot(configDir, root.Dir)
		if err != nil {
			return nil, err
		}
		pkg, err := pythonPackage(runRoot, root.Dir)
		if err != nil {
			return nil, err
		}
		if _, seen := imports[runRoot]; !seen {
			runRoots = append(runRoots, runRoot)
		}
		imports[runRoot] = append(imports[runRoot], pkg)
	}

	commands := make([]*tracing.Cmd, 0, len(runRoots))
	for _, runRoot := range runRoots {
		var source strings.Builder
		fmt.Fprintf(&source, "import sys\nfrom importlib import import_module\n\nsys.path.insert(0, %s)\n", strconv.Quote(runRoot))
		for _, pkg := range imports[runRoot] {
			fmt.Fprintf(&source, "import_module(%s)\n", strconv.Quote(pkg))
		}
		entry, err := writePythonEntry(runRoot, "discovery", source.String())
		if err != nil {
			return nil, err
		}

		cmd := tracing.Command(ctx, PythonInterpreter(runRoot), entry)
		cmd.Dir = runRoot
		cmd.Env = slices.Concat(os.Environ(), []string{"PYTHONDONTWRITEBYTECODE=1"}, server.Env())
		commands = append(commands, cmd)
	}
	return commands, nil
}

func writePythonEntry(runRoot, role, source string) (string, error) {
	entry, err := workdir.WriteShared(filepath.Join(runRoot, buildoutput.StateDir, role), role+".py", []byte(source))
	if err != nil {
		return "", fmt.Errorf("discovery: %w", err)
	}
	rel, err := filepath.Rel(runRoot, entry)
	if err != nil {
		return "", fmt.Errorf("discovery: %w", err)
	}
	return "./" + filepath.ToSlash(rel), nil
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
