package language

import (
	"errors"
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

var NodeEntrypoints = []string{
	"src/server.ts",
	"src/server.js",
	"src/index.ts",
	"src/index.js",
	"src/app.ts",
	"src/app.js",
	"index.ts",
	"index.js",
	"server.ts",
	"server.js",
	"app.ts",
	"app.js",
}

var nodeIndexes = []string{"index.mjs", "index.cjs"}

var nodeExtensions = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}

const PythonEntryFile = "main.py"

const goBuildOS = "linux"

const cargoEdition2015 = "2015"

func NodeEntrypoint(dir string) (string, bool) {
	for _, candidate := range NodeEntrypoints {
		if isRegularFile(filepath.Join(dir, filepath.FromSlash(candidate))) {
			return candidate, true
		}
	}
	return "", false
}

func Detect(dir, entrypoint string) (Language, bool, error) {
	found := Manifested(dir)
	switch len(found) {
	case 0:
		return "", false, nil
	case 1:
		return found[0], true, nil
	}
	app, err := appAmong(dir, found, entrypoint)
	var missing *NoAppError
	if errors.As(err, &missing) {
		return "", false, newAmbiguityError(dir, missing.inspections)
	}
	if err != nil {
		return "", false, err
	}
	return app.language, true, nil
}

func DetectApp(dir string) (framework string, err error) {
	found := Manifested(dir)
	if len(found) == 0 {
		return "", &NoAppError{Dir: dir, lacks: []string{"none of " + english.Or(ManifestNames())}}
	}
	app, err := appAmong(dir, found, "")
	return app.framework, err
}

func appAmong(dir string, found []Language, entrypoint string) (inspection, error) {
	inspections := make([]inspection, 0, len(found))
	for _, l := range found {
		i, err := inspect(dir, l, entrypoint)
		if err != nil {
			return inspection{}, err
		}
		inspections = append(inspections, i)
	}
	apps := slices.DeleteFunc(slices.Clone(inspections), func(i inspection) bool { return i.framework == "" })
	switch len(apps) {
	case 1:
		return apps[0], nil
	case 0:
		lacks := make([]string, 0, len(inspections))
		for _, i := range inspections {
			lacks = append(lacks, i.lack)
		}
		return inspection{}, &NoAppError{Dir: dir, lacks: lacks, inspections: inspections}
	default:
		return inspection{}, newAmbiguityError(dir, apps)
	}
}

type AmbiguityError struct {
	Dir       string
	Languages []Language
	holdings  []string
}

func newAmbiguityError(dir string, candidates []inspection) *AmbiguityError {
	e := &AmbiguityError{Dir: dir}
	for _, c := range candidates {
		e.Languages = append(e.Languages, c.language)
		e.holdings = append(e.holdings, c.holding)
	}
	return e
}

func (e *AmbiguityError) Holdings() string {
	if len(e.holdings) == 2 {
		return "both " + english.And(e.holdings)
	}
	return english.And(e.holdings)
}

func (e *AmbiguityError) Error() string {
	return fmt.Sprintf("%s holds %s, and nothing there says which one is the app", e.Dir, e.Holdings())
}

type NoAppError struct {
	Dir         string
	lacks       []string
	inspections []inspection
}

func (e *NoAppError) Holdings() string {
	return english.And(e.lacks)
}

func (e *NoAppError) Error() string {
	return fmt.Sprintf("%s holds %s, so no build there serves an app", e.Dir, e.Holdings())
}

type inspection struct {
	language  Language
	framework string
	holding   string
	lack      string
}

func inspect(dir string, l Language, entrypoint string) (inspection, error) {
	manifest := "a " + manifestOf(dir, l)
	app := func(framework, holding string) (inspection, error) {
		return inspection{language: l, framework: framework, holding: holding}, nil
	}
	none := func(lack string) (inspection, error) {
		return inspection{language: l, holding: manifest, lack: lack}, nil
	}
	if entrypoint != "" {
		return inspectEntrypoint(dir, l, entrypoint, manifest, app, none)
	}
	switch l {
	case JS:
		framework, node, err := readNodeApp(dir)
		if err != nil {
			return inspection{}, err
		}
		switch framework {
		case buildoutput.FrameworkNext:
			return app(framework, "a Next.js app")
		case buildoutput.FrameworkSvelteKit:
			return app(framework, "a SvelteKit app")
		}
		if served, ok := node.served(dir); ok {
			return app(buildoutput.FrameworkNode, "a node app ("+served+")")
		}
		return none(fmt.Sprintf("%s with no %s or %s dependency, no start script, no main and no entrypoint among %s", manifest, nextDependency, svelteKitDependency, strings.Join(append(slices.Clone(NodeEntrypoints), nodeIndexes...), ", ")))
	case Python:
		if isRegularFile(filepath.Join(dir, PythonEntryFile)) {
			return app(buildoutput.FrameworkPython, "a python app ("+PythonEntryFile+")")
		}
		return none(manifest + " with no " + PythonEntryFile)
	case Go:
		if isMainPackage(dir) {
			return app(buildoutput.FrameworkGo, "a go app (package main)")
		}
		return none(manifest + " with no package main in its own directory")
	case Rust:
		bins, err := crateBins(dir)
		if err != nil {
			return inspection{}, err
		}
		switch bins {
		case 1:
			return app(buildoutput.FrameworkRust, "a rust app (one binary)")
		case 0:
			return none(manifest + " building no binary")
		default:
			return none(fmt.Sprintf("%s building %d binaries, where an app builds one", manifest, bins))
		}
	}
	return none(manifest)
}

func inspectEntrypoint(dir string, l Language, entrypoint, manifest string, app func(string, string) (inspection, error), none func(string) (inspection, error)) (inspection, error) {
	path := filepath.Join(dir, filepath.FromSlash(entrypoint))
	switch l {
	case JS:
		if isRegularFile(path) && slices.Contains(nodeExtensions, filepath.Ext(path)) {
			return app(buildoutput.FrameworkNode, "a node app ("+entrypoint+")")
		}
		return none(manifest + ", and entrypoint " + entrypoint + " is no JavaScript or TypeScript file")
	case Go:
		if isMainPackage(path) || isMainFile(path) {
			return app(buildoutput.FrameworkGo, "a go app (package main in "+entrypoint+")")
		}
		return none(manifest + ", and entrypoint " + entrypoint + " holds no package main")
	}
	return none(manifest + ", whose build takes no entrypoint")
}

func manifestOf(dir string, l Language) string {
	for _, m := range manifests {
		if m.language == l && isRegularFile(filepath.Join(dir, m.name)) {
			return m.name
		}
	}
	return string(l) + " project"
}

func goBuildContext() build.Context {
	ctx := build.Default
	ctx.GOOS = goBuildOS
	ctx.CgoEnabled = false
	return ctx
}

func isMainPackage(dir string) bool {
	ctx := goBuildContext()
	pkg, err := ctx.ImportDir(dir, 0)
	return err == nil && pkg.Name == "main"
}

func isMainFile(path string) bool {
	if filepath.Ext(path) != ".go" || !isRegularFile(path) {
		return false
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly)
	return err == nil && file.Name.Name == "main"
}

type crateManifest struct {
	Package *struct {
		Name     string `toml:"name"`
		Autobins *bool  `toml:"autobins"`
		Edition  any    `toml:"edition"`
	} `toml:"package"`
	Workspace *struct {
		Package *struct {
			Edition string `toml:"edition"`
		} `toml:"package"`
	} `toml:"workspace"`
	Bins []struct {
		Name string `toml:"name"`
		Path string `toml:"path"`
	} `toml:"bin"`
}

func readCrate(path string) (crateManifest, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return crateManifest{}, fmt.Errorf("read %s: %w", path, err)
	}
	var crate crateManifest
	if err := toml.Unmarshal(body, &crate); err != nil {
		return crateManifest{}, fmt.Errorf("%s is not TOML: %w", path, err)
	}
	return crate, nil
}

func crateEdition(dir string, crate crateManifest) (string, error) {
	switch edition := crate.Package.Edition.(type) {
	case nil:
		return cargoEdition2015, nil
	case string:
		return edition, nil
	}
	for at := dir; ; at = filepath.Dir(at) {
		path := filepath.Join(at, rustManifest)
		if isRegularFile(path) {
			root, err := readCrate(path)
			if err != nil {
				return "", err
			}
			if root.Workspace != nil {
				if root.Workspace.Package == nil || root.Workspace.Package.Edition == "" {
					return "", fmt.Errorf("%s inherits its edition from the workspace at %s, which sets none", filepath.Join(dir, rustManifest), path)
				}
				return root.Workspace.Package.Edition, nil
			}
		}
		if filepath.Dir(at) == at {
			return "", fmt.Errorf("%s inherits its edition from a workspace, and no directory above it holds one", filepath.Join(dir, rustManifest))
		}
	}
}

func crateBins(dir string) (int, error) {
	crate, err := readCrate(filepath.Join(dir, rustManifest))
	if err != nil {
		return 0, err
	}
	if crate.Package == nil {
		return 0, nil
	}
	names := map[string]bool{}
	paths := map[string]bool{}
	for _, bin := range crate.Bins {
		binPath := bin.Path
		switch {
		case binPath != "":
		case bin.Name == crate.Package.Name:
			binPath = "src/main.rs"
		default:
			binPath = "src/bin/" + bin.Name + ".rs"
		}
		names[bin.Name] = true
		paths[filepath.Clean(filepath.FromSlash(binPath))] = true
	}
	if crate.Package.Autobins != nil && !*crate.Package.Autobins {
		return len(names), nil
	}
	if crate.Package.Autobins == nil && len(crate.Bins) > 0 {
		edition, err := crateEdition(dir, crate)
		if err != nil {
			return 0, err
		}
		if edition == cargoEdition2015 {
			return len(names), nil
		}
	}
	discovered := map[string]string{}
	if isRegularFile(filepath.Join(dir, "src", "main.rs")) {
		discovered[crate.Package.Name] = filepath.Join("src", "main.rs")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "src", "bin"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("read %s: %w", filepath.Join(dir, "src", "bin"), err)
	}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case !entry.IsDir() && filepath.Ext(name) == ".rs":
			discovered[strings.TrimSuffix(name, ".rs")] = filepath.Join("src", "bin", name)
		case entry.IsDir() && isRegularFile(filepath.Join(dir, "src", "bin", name, "main.rs")):
			discovered[name] = filepath.Join("src", "bin", name, "main.rs")
		}
	}
	for name, binPath := range discovered {
		if !names[name] && !paths[binPath] {
			names[name] = true
		}
	}
	return len(names), nil
}
