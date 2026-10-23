package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/ocelhq/ocel/cli/internal/pythonserver"
	"github.com/ocelhq/ocel/cli/internal/shellword"
)

const (
	pyprojectFile     = "pyproject.toml"
	uvLockFile        = "uv.lock"
	uvSync            = "UV_PYTHON_DOWNLOADS=never uv sync --locked --no-dev --no-editable"
	venvPython        = ".venv/bin/python3"
	pythonVersionFile = ".python-version"
	pythonTool        = "python"
)

var uvTool = Tool{Name: "uv", Version: "latest"}

type pyproject struct {
	Tool struct {
		UV struct {
			Sources map[string]any `toml:"sources"`
		} `toml:"uv"`
	} `toml:"tool"`
}

func readUvPathSources(dir string) ([]string, error) {
	named := filepath.Join(dir, pyprojectFile)
	read, err := os.ReadFile(named)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", named, err)
	}
	var project pyproject
	if err := toml.Unmarshal(read, &project); err != nil {
		return nil, fmt.Errorf("read %s: %w", named, err)
	}
	var dirs []string
	for _, declared := range project.Tool.UV.Sources {
		sources, ok := declared.([]any)
		if !ok {
			sources = []any{declared}
		}
		for _, source := range sources {
			if path, ok := findLocalPath(dir, source); ok {
				dirs = append(dirs, path)
			}
		}
	}
	return dirs, nil
}

func findPythonContext(dir string) (string, error) {
	var reached []string
	seen := map[string]bool{dir: true}
	for queue := []string{dir}; len(queue) > 0; queue = queue[1:] {
		sources, err := readUvPathSources(queue[0])
		if err != nil {
			return "", err
		}
		for _, source := range sources {
			if seen[source] {
				continue
			}
			seen[source] = true
			reached = append(reached, source)
			if info, err := os.Stat(source); err == nil && info.IsDir() {
				queue = append(queue, source)
			}
		}
	}
	root, outside := findContext(dir, reached)
	if outside != "" {
		repository := findRepository(dir)
		return "", fmt.Errorf(
			"the %s at %s names %s as a uv source, and the only directory holding both is %s, outside %s: an image is built from one directory holding the project and every package its uv sources name by path, and never from above the repository the app is checked out in or from the filesystem root — move %s inside %s, or depend on a published version of the package it holds",
			pyprojectFile, dir, outside, root, repository, outside, repository,
		)
	}
	return root, nil
}

func (l Location) isUvProjectBelowContext() bool {
	return l.Path != "." &&
		isRegularFile(filepath.Join(l.Dir(), pyprojectFile)) &&
		isRegularFile(filepath.Join(l.Dir(), uvLockFile))
}

func readPythonVersion(dir string) (string, error) {
	read, err := os.ReadFile(filepath.Join(dir, pythonVersionFile))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the %s in %s: %w", pythonVersionFile, dir, err)
	}
	for _, line := range strings.Split(string(read), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			return line, nil
		}
	}
	return "", nil
}

func (l Location) planPythonCommands() (Commands, error) {
	var commands Commands
	synced := l.isUvProjectBelowContext()
	if synced {
		commands = Commands{Install: l.inAppDir(uvSync), Tools: []Tool{uvTool}, InstallsInBuild: true}
		version, err := readPythonVersion(l.Dir())
		if err != nil {
			return Commands{}, err
		}
		if version != "" {
			commands.Tools = append(commands.Tools, Tool{Name: pythonTool, Version: version})
		}
	}
	if l.Entrypoint == "" {
		return commands, nil
	}
	resolved, err := pythonserver.ResolveEntrypoint(l.Dir(), l.Entrypoint)
	if err != nil {
		return Commands{}, fmt.Errorf("plan the python app's start command: %w", err)
	}
	words := resolved.Command()
	if synced {
		words[0] = venvPython
	}
	for i, word := range words {
		words[i] = shellword.Quote(word)
	}
	commands.Start = l.inAppDir(strings.Join(words, " "))
	commands.Asset = Asset{Name: pythonserver.File, Content: pythonserver.Source}
	return commands, nil
}
