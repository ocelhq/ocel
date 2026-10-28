package toolchain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/pkg/tracing"
)

const (
	pythonRequirementsFile = "requirements.txt"
	pythonProjectFile      = "pyproject.toml"
	pythonUvLockFile       = "uv.lock"
	pythonPipfile          = "Pipfile"
	pythonPipfileLock      = "Pipfile.lock"
)

type pythonDependencies struct {
	declaredIn   string
	requirements string
}

type pythonProject struct {
	Project *struct {
		Dependencies []string `toml:"dependencies"`
	} `toml:"project"`
	Tool struct {
		Poetry struct {
			Dependencies map[string]any `toml:"dependencies"`
		} `toml:"poetry"`
	} `toml:"tool"`
}

type pipfileLock struct {
	Default map[string]pipfileLocked `json:"default"`
}

type pipfileLocked struct {
	Version string   `json:"version"`
	Extras  []string `json:"extras"`
	Markers string   `json:"markers"`
	Path    string   `json:"path"`
	File    string   `json:"file"`
	Git     string   `json:"git"`
	Ref     string   `json:"ref"`
}

func (c Compilation) exportDependencies(ctx context.Context, scratch string) (pythonDependencies, error) {
	declared, err := c.declares(pythonRequirementsFile)
	if err != nil {
		return pythonDependencies{}, err
	}
	if declared {
		return pythonDependencies{declaredIn: pythonRequirementsFile, requirements: filepath.Join(c.Source, pythonRequirementsFile)}, nil
	}
	project, declared, err := c.readPythonProject()
	if err != nil {
		return pythonDependencies{}, err
	}
	if declared {
		locked, err := c.declares(pythonUvLockFile)
		if err != nil {
			return pythonDependencies{}, err
		}
		if locked {
			return c.exportUvLock(ctx, scratch)
		}
		if project.Project != nil {
			return writeRequirements(scratch, pythonProjectFile, project.Project.Dependencies)
		}
		if poetry := slices.DeleteFunc(slices.Collect(maps.Keys(project.Tool.Poetry.Dependencies)), func(name string) bool { return name == "python" }); len(poetry) > 0 {
			return pythonDependencies{}, fmt.Errorf("app %q declares its dependencies in the tool.poetry.dependencies of its %s, which ocel does not vendor: declare them as [project].dependencies, which Poetry 2 reads too, or export them to %s", c.App, pythonProjectFile, pythonRequirementsFile)
		}
	}
	locked, err := c.declares(pythonPipfileLock)
	if err != nil {
		return pythonDependencies{}, err
	}
	if locked {
		return c.exportPipfileLock(scratch)
	}
	unlocked, err := c.declares(pythonPipfile)
	if err != nil {
		return pythonDependencies{}, err
	}
	if unlocked {
		return pythonDependencies{}, fmt.Errorf("app %q declares its dependencies in a %s with no %s beside it, and ocel vendors the versions a lock pins: run pipenv lock and commit %s", c.App, pythonPipfile, pythonPipfileLock, pythonPipfileLock)
	}
	return pythonDependencies{}, nil
}

func (c Compilation) declares(name string) (bool, error) {
	declared := filepath.Join(c.Source, name)
	info, err := os.Stat(declared)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read app %q's %s: %w", c.App, declared, err)
	case !info.Mode().IsRegular():
		return false, fmt.Errorf("app %q has %s as %s, and the dependencies it vendors are read from a file", c.App, declared, info.Mode().Type())
	}
	return true, nil
}

func (c Compilation) readPythonProject() (pythonProject, bool, error) {
	declared, err := c.declares(pythonProjectFile)
	if err != nil || !declared {
		return pythonProject{}, false, err
	}
	read, err := os.ReadFile(filepath.Join(c.Source, pythonProjectFile))
	if err != nil {
		return pythonProject{}, false, err
	}
	var project pythonProject
	if err := toml.Unmarshal(read, &project); err != nil {
		return pythonProject{}, false, fmt.Errorf("read app %q's %s: %w", c.App, pythonProjectFile, err)
	}
	return project, true, nil
}

func (c Compilation) exportUvLock(ctx context.Context, scratch string) (pythonDependencies, error) {
	uv, err := exec.LookPath("uv")
	if err != nil {
		return pythonDependencies{}, fmt.Errorf("app %q locks its dependencies in %s, and uv is not on PATH to export them for pip: install uv (https://docs.astral.sh/uv/) where ocel builds", c.App, pythonUvLockFile)
	}
	requirements := filepath.Join(scratch, pythonRequirementsFile)
	cmd := tracing.Command(ctx, uv, "export", "--frozen", "--format", "requirements-txt", "--no-dev", "--no-editable", "--no-hashes", "--no-header", "--no-annotate", "--output-file", requirements)
	cmd.Dir = c.Source
	var said bytes.Buffer
	cmd.Stdout = &said
	cmd.Stderr = &said
	if err := childprocess.Run(ctx, cmd); err != nil {
		return pythonDependencies{}, fmt.Errorf("export app %q's %s with uv (%w):\n%s", c.App, pythonUvLockFile, err, said.String())
	}
	return pythonDependencies{declaredIn: pythonUvLockFile, requirements: requirements}, nil
}

func (c Compilation) exportPipfileLock(scratch string) (pythonDependencies, error) {
	read, err := os.ReadFile(filepath.Join(c.Source, pythonPipfileLock))
	if err != nil {
		return pythonDependencies{}, err
	}
	var lock pipfileLock
	if err := json.Unmarshal(read, &lock); err != nil {
		return pythonDependencies{}, fmt.Errorf("read app %q's %s: %w", c.App, pythonPipfileLock, err)
	}
	var lines []string
	for _, name := range slices.Sorted(maps.Keys(lock.Default)) {
		lines = append(lines, lock.Default[name].requirement(name))
	}
	return writeRequirements(scratch, pythonPipfileLock, lines)
}

func (p pipfileLocked) requirement(name string) string {
	extras := ""
	if len(p.Extras) > 0 {
		extras = "[" + strings.Join(p.Extras, ",") + "]"
	}
	var line string
	switch {
	case p.Path != "":
		line = p.Path + extras
	case p.Git != "":
		line = name + extras + " @ git+" + strings.TrimPrefix(p.Git, "git+")
		if p.Ref != "" {
			line += "@" + p.Ref
		}
	case p.File != "":
		line = name + extras + " @ " + p.File
	default:
		line = name + extras + strings.TrimPrefix(p.Version, "*")
	}
	if p.Markers != "" {
		line += " ; " + p.Markers
	}
	return line
}

func writeRequirements(scratch, declaredIn string, lines []string) (pythonDependencies, error) {
	if len(lines) == 0 {
		return pythonDependencies{}, nil
	}
	requirements := filepath.Join(scratch, pythonRequirementsFile)
	if err := os.WriteFile(requirements, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return pythonDependencies{}, err
	}
	return pythonDependencies{declaredIn: declaredIn, requirements: requirements}, nil
}
