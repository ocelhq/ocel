package jobinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/workspace"
	"github.com/ocelhq/ocel/pkg/runner/agent"
)

// TODO(ocelhq/ocel#2091): delete this package and agent.Config.FindInstalls once discovery installs a JS project's dependencies itself.
var frozenInstalls = map[workspace.PackageManager][]string{
	workspace.Pnpm:        {"pnpm", "install", "--frozen-lockfile"},
	workspace.Npm:         {"npm", "ci"},
	workspace.YarnBerry:   {"yarn", "install", "--immutable"},
	workspace.YarnClassic: {"corepack", "yarn@1.22.22", "install", "--frozen-lockfile"},
	workspace.Bun:         {"bun", "install", "--frozen-lockfile"},
}

var installedMarkers = []string{"node_modules", ".pnp.cjs", ".pnp.js"}

func Find(ctx context.Context, source, projectDir string) ([]agent.Install, error) {
	roots, err := jsRoots(ctx, source, projectDir)
	if err != nil {
		return nil, err
	}
	var missing []agent.Install
	for _, root := range roots {
		found, ok, err := findInstall(source, root)
		if err != nil {
			return nil, err
		}
		if ok && !slices.ContainsFunc(missing, func(i agent.Install) bool { return i.Dir == found.Dir }) {
			missing = append(missing, found)
		}
	}
	return missing, nil
}

func jsRoots(ctx context.Context, source, projectDir string) ([]string, error) {
	configPath, err := project.FindConfig(projectDir, "")
	if err != nil {
		return nil, err
	}
	var dirs []string
	var paths []string
	switch {
	case configPath == "":
	case project.IsTypeScript(configPath):
		dirs = append(dirs, filepath.Dir(configPath))
	default:
		cfg, err := project.Load(ctx, projectDir, "")
		if err != nil {
			return nil, err
		}
		paths = cfg.DiscoveryPaths
	}
	roots, err := discovery.Roots(projectDir, paths)
	if err != nil {
		return nil, err
	}
	for _, root := range roots {
		if root.Language == language.JS && within(source, root.Dir) && !slices.Contains(dirs, root.Dir) {
			dirs = append(dirs, root.Dir)
		}
	}
	return dirs, nil
}

func findInstall(source, dir string) (agent.Install, bool, error) {
	pkg, ok := nearestPackage(source, dir)
	if !ok {
		return agent.Install{}, false, nil
	}
	root := pkg
	if enclosing, ok := workspace.Enclosing(pkg); ok && within(source, enclosing) {
		root = enclosing
	}
	declared, err := declaresDependencies(pkg, root)
	if err != nil || !declared || installedFrom(source, pkg) {
		return agent.Install{}, false, err
	}
	args := frozenInstalls[workspace.Detect(root)]
	if args == nil {
		return agent.Install{}, false, fmt.Errorf(
			"the dependencies of %s are not installed, and %s holds no lockfile to install them from: install them in %s with your package manager, and commit the lockfile it writes",
			relative(source, pkg), relative(source, root), relative(source, root),
		)
	}
	return agent.Install{Dir: root, Args: args}, true, nil
}

func within(source, dir string) bool {
	rel, err := filepath.Rel(source, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func relative(source, dir string) string {
	rel, err := filepath.Rel(source, dir)
	if err != nil || rel == "." {
		return "the project's root"
	}
	return filepath.ToSlash(rel)
}

func nearestPackage(source, dir string) (string, bool) {
	for ; ; dir = filepath.Dir(dir) {
		if isRegularFile(filepath.Join(dir, "package.json")) {
			return dir, true
		}
		if dir == source || filepath.Dir(dir) == dir {
			return "", false
		}
	}
}

func installedFrom(source, pkg string) bool {
	for dir := pkg; ; dir = filepath.Dir(dir) {
		for _, marker := range installedMarkers {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return true
			}
		}
		if dir == source || filepath.Dir(dir) == dir {
			return false
		}
	}
}

func declaresDependencies(dirs ...string) (bool, error) {
	for _, dir := range dirs {
		read, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("read the package.json in %s: %w", dir, err)
		}
		var declared struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if err := json.Unmarshal(read, &declared); err != nil {
			return false, fmt.Errorf("read the package.json in %s: %w", dir, err)
		}
		if len(declared.Dependencies) > 0 || len(declared.DevDependencies) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
