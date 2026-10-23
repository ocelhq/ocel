package workspace

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/ocelhq/ocel/cli/internal/gomodule"
	"github.com/ocelhq/ocel/cli/internal/language"
	"gopkg.in/yaml.v3"
)

const (
	manifestName      = "package.json"
	pnpmWorkspaceFile = "pnpm-workspace.yaml"
	gitEntry          = ".git"
	vendorDir         = "node_modules"
)

type Package struct {
	Name  string
	Build bool
	Start bool
	Main  string
	Index string
}

type Program struct {
	Language   language.Language
	GoModule   *gomodule.Module
	GoPackage  string
	Entrypoint string
}

type Location struct {
	Root           string
	Path           string
	Member         bool
	Language       language.Language
	PackageManager PackageManager
	Package        Package
	BuildCommand   string
	ModuleDir      string
	GoVersion      string
	GoPackage      string
	Entrypoint     string
}

func (l Location) Dir() string { return filepath.Join(l.Root, filepath.FromSlash(l.Path)) }

func (l Location) Rebase(context string) (Location, error) {
	root, err := filepath.Abs(context)
	if err != nil {
		return Location{}, err
	}
	if !under(root, l.Root) {
		return Location{}, fmt.Errorf(
			"%s is neither %s nor a directory it sits under: an image is built from a context containing everything the build reads, so image.context may name %s — the app's own directory, the workspace it is a member of, or the directory holding it and everything it depends on by path, such as the modules its go.mod replaces or the crates its Cargo.toml names — or a directory above it",
			root, l.Root, l.Root,
		)
	}
	var rebased Location
	if l.ModuleDir != "" {
		rebased, err = locateGoApp(l.Dir(), filepath.Join(l.Root, filepath.FromSlash(l.ModuleDir)), l.GoVersion, root)
	} else {
		rebased, err = locatedAt(l.Dir(), root, l.Language)
	}
	if err != nil {
		return Location{}, err
	}
	rebased.BuildCommand, rebased.GoPackage, rebased.Entrypoint = l.BuildCommand, l.GoPackage, l.Entrypoint
	return rebased, nil
}

func under(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	slashed := filepath.ToSlash(rel)
	return slashed != ".." && !strings.HasPrefix(slashed, "../")
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

func Locate(appDir string, program Program) (Location, error) {
	dir, err := filepath.Abs(appDir)
	if err != nil {
		return Location{}, err
	}
	located, err := locate(dir, program)
	if err != nil {
		return Location{}, err
	}
	located.GoPackage, located.Entrypoint = program.GoPackage, program.Entrypoint
	return located, nil
}

func locate(dir string, program Program) (Location, error) {
	if module := program.GoModule; program.Language == language.Go && module != nil {
		root, err := findGoContext(*module)
		if err != nil {
			return Location{}, err
		}
		return locateGoApp(dir, module.Dir, module.GoVersion, root)
	}
	root := dir
	switch program.Language {
	case "", language.JS:
		if enclosingRoot, ok := enclosing(dir); ok {
			root = enclosingRoot
		}
	case language.Rust:
		found, err := findRustContext(dir)
		if err != nil {
			return Location{}, err
		}
		root = found
	case language.Python:
		found, err := findPythonContext(dir)
		if err != nil {
			return Location{}, err
		}
		root = found
	}
	return locatedAt(dir, root, program.Language)
}

func findContext(dir string, reached []string) (root, outside string) {
	repository := findRepository(dir)
	root = dir
	for _, path := range reached {
		for !under(root, path) {
			root = filepath.Dir(root)
		}
		if !under(repository, root) || filepath.Dir(root) == root {
			return root, path
		}
	}
	return root, ""
}

func findLocalPath(dir string, declared any) (string, bool) {
	table, _ := declared.(map[string]any)
	named, ok := table["path"].(string)
	if !ok || named == "" {
		return "", false
	}
	if !filepath.IsAbs(named) {
		named = filepath.Join(dir, filepath.FromSlash(named))
	}
	return filepath.Clean(named), true
}

func findRepository(dir string) string {
	for !atBoundary(dir) {
		dir = filepath.Dir(dir)
	}
	return dir
}

func findRelativeRoot(dir string) string {
	if dir == "." {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("../", strings.Count(dir, "/")+1), "/")
}

func locatedAt(dir, root string, written language.Language) (Location, error) {
	app, err := readManifest(filepath.Join(dir, manifestName))
	if err != nil {
		return Location{}, fmt.Errorf("read the %s of the app in %s: %w", manifestName, dir, err)
	}

	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return Location{}, err
	}
	located := Location{
		Root:           root,
		Path:           filepath.ToSlash(rel),
		Language:       written,
		Package:        describe(dir, app),
		PackageManager: detect(root),
	}
	if globs, ok := declaredPackages(root); ok && (written == "" || written == language.JS) {
		_, located.Member = memberOf(root, dir, globs)
	}

	if dep := workspaceDependency(app); dep != "" && located.PackageManager == Unknown {
		return Location{}, fmt.Errorf(
			"app %q depends on %q as %q, and %s contains no lockfile: the image installs the app's dependencies from one, and no installer resolves a workspace: range without it — install in %s so it writes a %s, %s, %s or %s, and commit what it writes",
			appName(app, dir), dep, workspaceRange(app, dep), located.Root, located.Root,
			pnpmLock, npmLock, yarnLock, bunLock,
		)
	}
	return located, nil
}

func (l Location) Members() []string {
	members := slices.Collect(maps.Keys(l.memberManifests()))
	slices.Sort(members)
	return members
}

var bunCommand = regexp.MustCompile(`(^|\s|;|&|&&|\||\|\|)bunx?\s`)

func (l Location) HasScriptCallingBun() (bool, error) {
	app, err := readManifest(filepath.Join(l.Dir(), manifestName))
	if err != nil {
		return false, fmt.Errorf("read the %s of the app in %s: %w", manifestName, l.Dir(), err)
	}
	members := l.memberManifests()
	reached := []manifest{app}
	seen := map[string]bool{app.Name: true}
	for len(reached) > 0 {
		m := reached[0]
		reached = reached[1:]
		for _, script := range m.Scripts {
			if bunCommand.MatchString(script) {
				return true, nil
			}
		}
		for _, deps := range []map[string]string{m.Dependencies, m.DevDeps} {
			for name := range deps {
				member, ok := members[name]
				if !ok || seen[name] {
					continue
				}
				seen[name] = true
				reached = append(reached, member)
			}
		}
	}
	return false, nil
}

func (l Location) memberManifests() map[string]manifest {
	globs, ok := declaredPackages(l.Root)
	if !ok {
		return nil
	}
	tree := os.DirFS(l.Root)
	named := map[string]manifest{}
	for _, glob := range globs {
		pattern := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(glob), "./"), "/")
		if strings.HasPrefix(pattern, "!") {
			continue
		}
		matches, err := doublestar.Glob(tree, path.Join(pattern, manifestName))
		if err != nil {
			continue
		}
		for _, match := range matches {
			dir := path.Dir(match)
			if slices.Contains(strings.Split(dir, "/"), vendorDir) {
				continue
			}
			if _, member := memberOf(l.Root, filepath.Join(l.Root, filepath.FromSlash(dir)), globs); !member {
				continue
			}
			m, err := readManifest(filepath.Join(l.Root, filepath.FromSlash(match)))
			if err != nil || m.Name == "" {
				continue
			}
			named[m.Name] = m
		}
	}
	return named
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func describe(dir string, m manifest) Package {
	app := Package{Name: m.Name, Main: m.Main}
	app.Build = strings.TrimSpace(m.Scripts["build"]) != ""
	app.Start = strings.TrimSpace(m.Scripts["start"]) != ""
	for _, name := range []string{"index.js", "index.mjs", "index.cjs", "index.ts"} {
		if isRegularFile(filepath.Join(dir, name)) {
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

func enclosing(appDir string) (string, bool) {
	for dir := filepath.Dir(appDir); ; dir = filepath.Dir(dir) {
		if globs, ok := declaredPackages(dir); ok {
			if _, member := memberOf(dir, appDir, globs); member {
				return dir, true
			}
		}
		if atBoundary(dir) {
			return "", false
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
