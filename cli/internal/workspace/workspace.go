package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v2"
)

const (
	manifestName      = "package.json"
	pnpmWorkspaceFile = "pnpm-workspace.yaml"
	gitEntry          = ".git"
)

type App struct {
	Name  string
	Build bool
	Start bool
	Main  string
	Index string
}

type Location struct {
	Root         string
	Path         string
	Manager      Manager
	App          App
	BuildCommand string
}

func (l Location) InWorkspace() bool { return l.Path != "." }

func (l Location) Rebase(root string) (Location, error) {
	appDir := filepath.Join(l.Root, filepath.FromSlash(l.Path))
	rel, err := filepath.Rel(root, appDir)
	if err != nil || rel == ".." || strings.HasPrefix(filepath.ToSlash(rel), "../") {
		return Location{}, fmt.Errorf("%s is not inside %s, and an image is built from a directory holding the app it serves: point the build context at a directory %s sits under", appDir, root, appDir)
	}
	l.Root, l.Path, l.Manager = root, filepath.ToSlash(rel), detect(root)
	return l, nil
}

type manifest struct {
	Name           string            `json:"name"`
	PackageManager string            `json:"packageManager"`
	Workspaces     json.RawMessage   `json:"workspaces"`
	Scripts        map[string]string `json:"scripts"`
	Main           string            `json:"main"`
	Dependencies   map[string]string `json:"dependencies"`
	DevDeps        map[string]string `json:"devDependencies"`
}

func Locate(appDir string) (Location, error) {
	dir, err := filepath.Abs(appDir)
	if err != nil {
		return Location{}, err
	}
	app, err := readManifest(filepath.Join(dir, manifestName))
	if err != nil {
		return Location{}, fmt.Errorf("read the %s of the app in %s: %w", manifestName, dir, err)
	}

	located := Location{Root: dir, Path: ".", App: describe(dir, app)}
	if root, rel, ok := enclosing(dir); ok {
		located.Root, located.Path = root, rel
	}
	located.Manager = detect(located.Root)

	if dep := workspaceDependency(app); dep != "" && located.Manager == Unknown {
		return Location{}, fmt.Errorf(
			"app %q depends on %q as %q, and %s holds no lockfile: the image installs the app's dependencies from one, and no installer resolves a workspace: range without it — install in %s so it writes a %s, %s, %s or %s, and commit what it writes",
			appName(app, dir), dep, workspaceRange(app, dep), located.Root, located.Root,
			pnpmLock, npmLock, yarnLock, bunLock,
		)
	}
	return located, nil
}

func describe(dir string, m manifest) App {
	app := App{Name: m.Name, Main: m.Main}
	app.Build = strings.TrimSpace(m.Scripts["build"]) != ""
	app.Start = strings.TrimSpace(m.Scripts["start"]) != ""
	for _, name := range []string{"index.js", "index.mjs", "index.cjs", "index.ts"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Mode().IsRegular() {
			app.Index = name
			break
		}
	}
	return app
}

func appName(m manifest, dir string) string {
	if m.Name != "" {
		return m.Name
	}
	return filepath.Base(dir)
}

func enclosing(appDir string) (string, string, bool) {
	for dir := filepath.Dir(appDir); ; dir = filepath.Dir(dir) {
		if globs, ok := declaredPackages(dir); ok {
			if rel, member := memberOf(dir, appDir, globs); member {
				return dir, rel, true
			}
		}
		if atBoundary(dir) {
			return "", "", false
		}
	}
}

func atBoundary(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, gitEntry)); err == nil {
		return true
	}
	return filepath.Dir(dir) == dir
}

func declaredPackages(dir string) ([]string, bool) {
	if globs, ok := pnpmPackages(dir); ok {
		return globs, true
	}
	m, err := readManifest(filepath.Join(dir, manifestName))
	if err != nil || len(m.Workspaces) == 0 {
		return nil, false
	}
	var globs []string
	if err := json.Unmarshal(m.Workspaces, &globs); err == nil {
		return globs, len(globs) > 0
	}
	var nested struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(m.Workspaces, &nested); err != nil {
		return nil, false
	}
	return nested.Packages, len(nested.Packages) > 0
}

func pnpmPackages(dir string) ([]string, bool) {
	read, err := os.ReadFile(filepath.Join(dir, pnpmWorkspaceFile))
	if err != nil {
		return nil, false
	}
	var declared struct {
		Packages []string `yaml:"packages"`
	}
	if err := yaml.Unmarshal(read, &declared); err != nil {
		return nil, false
	}
	return declared.Packages, len(declared.Packages) > 0
}

func memberOf(root, appDir string, globs []string) (string, bool) {
	rel, err := filepath.Rel(root, appDir)
	if err != nil {
		return "", false
	}
	slashed := filepath.ToSlash(rel)
	if slashed == "." || strings.HasPrefix(slashed, "../") {
		return "", false
	}
	member := false
	for _, glob := range globs {
		pattern := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(glob), "./"), "/")
		excluding := strings.HasPrefix(pattern, "!")
		pattern = strings.TrimPrefix(pattern, "!")
		if pattern == slashed {
			member = !excluding
			continue
		}
		if ok, _ := doublestar.Match(pattern, slashed); ok {
			member = !excluding
		}
	}
	return slashed, member
}

func readManifest(path string) (manifest, error) {
	read, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return manifest{}, nil
		}
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(read, &m); err != nil {
		return manifest{}, err
	}
	return m, nil
}

func workspaceDependency(m manifest) string {
	for _, deps := range []map[string]string{m.Dependencies, m.DevDeps} {
		for name, constraint := range deps {
			if strings.HasPrefix(constraint, "workspace:") {
				return name
			}
		}
	}
	return ""
}

func workspaceRange(m manifest, dep string) string {
	for _, deps := range []map[string]string{m.Dependencies, m.DevDeps} {
		if constraint, ok := deps[dep]; ok {
			return constraint
		}
	}
	return ""
}
