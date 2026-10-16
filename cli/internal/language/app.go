package language

import (
	"errors"
	"fmt"
	"go/build"
	"os"
	"path/filepath"
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

const PythonEntryFile = "main.py"

const goBuildOS = "linux"

func NodeEntrypoint(dir string) (string, bool) {
	for _, candidate := range NodeEntrypoints {
		if isRegularFile(filepath.Join(dir, filepath.FromSlash(candidate))) {
			return candidate, true
		}
	}
	return "", false
}

func Detect(dir string) (Language, bool, error) {
	found := Manifested(dir)
	if len(found) == 0 {
		return "", false, nil
	}
	if len(found) == 1 {
		return found[0], true, nil
	}
	inspections, err := inspectAll(dir, found)
	if err != nil {
		return "", false, err
	}
	apps := holdingApps(inspections)
	if len(apps) == 1 {
		return apps[0].language, true, nil
	}
	if len(apps) == 0 {
		apps = inspections
	}
	return "", false, newAmbiguityError(dir, apps)
}

func DetectApp(dir string) (Language, error) {
	found := Manifested(dir)
	if len(found) == 0 {
		return "", &NoAppError{Dir: dir, lacks: []string{"none of " + english.Or(ManifestNames())}}
	}
	inspections, err := inspectAll(dir, found)
	if err != nil {
		return "", err
	}
	apps := holdingApps(inspections)
	switch len(apps) {
	case 1:
		return apps[0].language, nil
	case 0:
		lacks := make([]string, 0, len(inspections))
		for _, i := range inspections {
			lacks = append(lacks, i.lack)
		}
		return "", &NoAppError{Dir: dir, lacks: lacks}
	default:
		return "", newAmbiguityError(dir, apps)
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
	Dir   string
	lacks []string
}

func (e *NoAppError) Holdings() string {
	return english.And(e.lacks)
}

func (e *NoAppError) Error() string {
	return fmt.Sprintf("%s holds %s, so no build there serves an app", e.Dir, e.Holdings())
}

type inspection struct {
	language Language
	holdsApp bool
	holding  string
	lack     string
}

func holdingApps(inspections []inspection) []inspection {
	var apps []inspection
	for _, i := range inspections {
		if i.holdsApp {
			apps = append(apps, i)
		}
	}
	return apps
}

func inspectAll(dir string, found []Language) ([]inspection, error) {
	inspections := make([]inspection, 0, len(found))
	for _, l := range found {
		i, err := inspect(dir, l)
		if err != nil {
			return nil, err
		}
		inspections = append(inspections, i)
	}
	return inspections, nil
}

func inspect(dir string, l Language) (inspection, error) {
	manifest := "a " + manifestOf(dir, l)
	app := func(holding string) (inspection, error) {
		return inspection{language: l, holdsApp: true, holding: holding}, nil
	}
	none := func(lack string) (inspection, error) {
		return inspection{language: l, holding: manifest, lack: lack}, nil
	}
	switch l {
	case JS:
		framework, err := jsFramework(dir)
		if err != nil {
			return inspection{}, err
		}
		switch framework {
		case buildoutput.FrameworkNext:
			return app("a Next.js app")
		case buildoutput.FrameworkSvelteKit:
			return app("a SvelteKit app")
		}
		if entrypoint, ok := NodeEntrypoint(dir); ok {
			return app("a node app (" + entrypoint + ")")
		}
		return none(fmt.Sprintf("%s with no %s or %s dependency and no entrypoint among %s", manifest, nextDependency, svelteKitDependency, strings.Join(NodeEntrypoints, ", ")))
	case Python:
		if isRegularFile(filepath.Join(dir, PythonEntryFile)) {
			return app("a python app (" + PythonEntryFile + ")")
		}
		return none(manifest + " with no " + PythonEntryFile)
	case Go:
		if isMainPackage(dir) {
			return app("a go app (package main)")
		}
		return none(manifest + " with no package main in its own directory")
	case Rust:
		bins, err := crateBins(dir)
		if err != nil {
			return inspection{}, err
		}
		switch bins {
		case 1:
			return app("a rust app (one binary)")
		case 0:
			return none(manifest + " building no binary")
		default:
			return none(fmt.Sprintf("%s building %d binaries, where an app builds one", manifest, bins))
		}
	}
	return none(manifest)
}

func manifestOf(dir string, l Language) string {
	for _, m := range manifests {
		if m.language == l && isRegularFile(filepath.Join(dir, m.name)) {
			return m.name
		}
	}
	return string(l) + " project"
}

func isMainPackage(dir string) bool {
	ctx := build.Default
	ctx.GOOS = goBuildOS
	pkg, err := ctx.ImportDir(dir, 0)
	return err == nil && pkg.Name == "main"
}

type crateManifest struct {
	Package *struct {
		Name     string `toml:"name"`
		Autobins *bool  `toml:"autobins"`
	} `toml:"package"`
	Bins []struct {
		Name string `toml:"name"`
		Path string `toml:"path"`
	} `toml:"bin"`
}

func crateBins(dir string) (int, error) {
	path := filepath.Join(dir, rustManifest)
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	var crate crateManifest
	if err := toml.Unmarshal(body, &crate); err != nil {
		return 0, fmt.Errorf("%s is not TOML: %w", path, err)
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
