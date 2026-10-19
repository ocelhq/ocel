package language

import (
	"encoding/json"
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"slices"

	"github.com/pelletier/go-toml/v2"

	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/gomodule"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

const (
	nodeManifest        = "package.json"
	nextDependency      = "next"
	svelteKitDependency = "@sveltejs/kit"
	nestDependency      = "@nestjs/core"
	goBuildOS           = "linux"
)

var nextConfigNames = []string{"next.config.js", "next.config.mjs", "next.config.ts"}

var (
	NodeEntrypointBases      = []string{"src/server", "src/index", "src/app", "index", "server", "app", "src/main"}
	NodeEntrypointExtensions = []string{".ts", ".mts", ".cts", ".js", ".mjs", ".cjs"}
	nodeEntrypointFiles      = nodeEntrypoints()
)

var (
	PythonEntrypointDirs  = []string{"", "src/", "app/"}
	PythonEntrypointFiles = []string{"app.py", "index.py", "server.py", "main.py", "wsgi.py", "asgi.py"}
	PythonEntrypoints     = pythonEntrypoints()
)

var goEntrypoints = []string{".", "cmd/api", "cmd/server"}

func pythonEntrypointPlaces() []string {
	places := make([]string, 0, len(PythonEntrypointDirs))
	for _, dir := range PythonEntrypointDirs {
		if dir == "" {
			places = append(places, "at its root")
			continue
		}
		places = append(places, "in "+dir)
	}
	return places
}

func nodeEntrypoints() []string {
	var candidates []string
	for _, base := range NodeEntrypointBases {
		for _, extension := range NodeEntrypointExtensions {
			candidates = append(candidates, base+extension)
		}
	}
	return candidates
}

func pythonEntrypoints() []string {
	var candidates []string
	for _, dir := range PythonEntrypointDirs {
		for _, file := range PythonEntrypointFiles {
			candidates = append(candidates, dir+file)
		}
	}
	return candidates
}

type Source string

const (
	Detected   Source = "detected"
	Configured Source = "config"
)

type Detection struct {
	Framework  string
	Language   Language
	Entrypoint string
	Evidence   string
	Source     Source
	Manifest   string
	GoModule   *gomodule.Module
	Lacking    string
}

type manifest struct {
	language Language
	files    []string
	read     func(path string) (contents, error)
	open     func(dir, root string) (opened, bool, error)
	claims   func(dir string) bool
}

type opened struct {
	name string
	contents
}

type contents struct {
	dependencies map[string]bool
	crate        crateManifest
	module       *gomodule.Module
}

type crateManifest struct {
	Package *struct {
		Name string `toml:"name"`
	} `toml:"package"`
	Bins []struct {
		Name string `toml:"name"`
	} `toml:"bin"`
}

var (
	nodePackage   = &manifest{language: JS, files: []string{nodeManifest}, read: readPackage}
	pythonProject = &manifest{language: Python, files: []string{"pyproject.toml", "requirements.txt", "Pipfile"}}
	goModule      = &manifest{language: Go, files: []string{gomodule.File}, open: openGoModule, claims: hasGoModuleOrSource}
	rustCrate     = &manifest{language: Rust, files: []string{"Cargo.toml"}, read: readCrate}
)

var manifests = []*manifest{nodePackage, pythonProject, goModule, rustCrate}

type detector struct {
	framework  string
	label      string
	manifest   *manifest
	supersedes []string
	lacks      string
	match      func(dir string, c contents) (evidence, entrypoint string)
}

var detectors = []detector{
	{
		framework:  buildoutput.FrameworkNext,
		label:      "a Next.js app",
		manifest:   nodePackage,
		supersedes: []string{buildoutput.FrameworkNode},
		lacks:      "no " + nextDependency + " dependency or next config",
		match:      matchNext,
	},
	{
		framework:  buildoutput.FrameworkSvelteKit,
		label:      "a SvelteKit app",
		manifest:   nodePackage,
		supersedes: []string{buildoutput.FrameworkNode},
		lacks:      "no " + svelteKitDependency + " dependency",
		match:      matchSvelteKit,
	},
	{
		framework:  buildoutput.FrameworkNestJS,
		label:      "a NestJS app",
		manifest:   nodePackage,
		supersedes: []string{buildoutput.FrameworkNode},
		lacks:      "no " + nestDependency + " dependency",
		match:      matchNestJS,
	},
	{
		framework: buildoutput.FrameworkNode,
		label:     "a node app",
		manifest:  nodePackage,
		lacks:     "no entrypoint among " + english.Or(NodeEntrypointBases) + " with a " + english.Or(NodeEntrypointExtensions) + " extension",
		match:     firstFile(nodeEntrypointFiles),
	},
	{
		framework: buildoutput.FrameworkPython,
		label:     "a python app",
		manifest:  pythonProject,
		lacks:     "no " + english.Or(PythonEntrypointFiles) + " " + english.Or(pythonEntrypointPlaces()),
		match:     firstFile(PythonEntrypoints),
	},
	{
		framework: buildoutput.FrameworkGo,
		label:     "a go app",
		manifest:  goModule,
		lacks:     "no package main in " + english.Or(goEntrypoints),
		match:     matchGo,
	},
	{
		framework: buildoutput.FrameworkRust,
		label:     "a rust app",
		manifest:  rustCrate,
		lacks:     "no binary target",
		match:     matchRust,
	},
}

func Resolve(dir, root, named string) (Detection, error) {
	place := placeOf(dir, root)
	if named == "" {
		return detect(dir, root, place)
	}
	at := slices.IndexFunc(detectors, func(d detector) bool { return d.framework == named })
	if at < 0 {
		return Detection{}, fmt.Errorf("framework %q is none ocel builds", named)
	}
	d := detectors[at]
	found, present, err := d.manifest.find(dir, root)
	if err != nil {
		return Detection{}, &UndetectedAppError{Place: place, Misses: []Miss{{Manifest: found.name, Language: d.manifest.language, Unreadable: err}}}
	}
	evidence, entrypoint := d.match(dir, found.contents)
	resolved := Detection{Framework: named, Language: d.manifest.language, Entrypoint: entrypoint, Evidence: evidence, Source: Configured, GoModule: found.module}
	if present {
		resolved.Manifest = found.name
	}
	if evidence == "" {
		miss := Miss{Manifest: resolved.Manifest, Language: d.manifest.language, Frameworks: []string{named}}
		resolved.Lacking = (&UndetectedAppError{Place: place, Misses: []Miss{miss}}).Error()
	}
	return resolved, nil
}

func detect(dir, root, place string) (Detection, error) {
	var matches []Detection
	var misses []Miss
	for _, m := range manifests {
		if m.claims != nil && !m.claims(dir) {
			continue
		}
		found, present, err := m.find(dir, root)
		if err != nil {
			misses = append(misses, Miss{Manifest: found.name, Language: m.language, Unreadable: err})
			continue
		}
		if !present {
			continue
		}
		var lacked []string
		before := len(matches)
		for _, d := range detectors {
			if d.manifest != m {
				continue
			}
			evidence, entrypoint := d.match(dir, found.contents)
			if evidence == "" {
				lacked = append(lacked, d.framework)
				continue
			}
			matches = append(matches, Detection{Framework: d.framework, Language: m.language, Entrypoint: entrypoint, Evidence: evidence, Source: Detected, Manifest: found.name, GoModule: found.module})
		}
		if len(matches) == before {
			misses = append(misses, Miss{Manifest: found.name, Language: m.language, Frameworks: lacked, GoModule: found.module})
		}
	}
	matches = withoutSuperseded(matches)
	if len(matches) == 1 {
		return matches[0], nil
	}
	return Detection{}, &UndetectedAppError{Place: place, Matches: matches, Misses: misses}
}

func placeOf(dir, root string) string {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(dir))
	switch {
	case err != nil:
		return dir
	case rel == ".":
		return "the project root"
	}
	return filepath.ToSlash(rel)
}

func detectorOf(framework string) detector {
	at := slices.IndexFunc(detectors, func(d detector) bool { return d.framework == framework })
	return detectors[at]
}

func withoutSuperseded(matches []Detection) []Detection {
	all := slices.Clone(matches)
	return slices.DeleteFunc(matches, func(candidate Detection) bool {
		return slices.ContainsFunc(all, func(other Detection) bool {
			return slices.Contains(detectorOf(other.Framework).supersedes, candidate.Framework)
		})
	})
}

type Miss struct {
	Manifest   string
	Language   Language
	Unreadable error
	Frameworks []string
	GoModule   *gomodule.Module
}

func (m Miss) describe() string {
	if m.Unreadable != nil {
		return "a " + m.Manifest + " that cannot be read: " + m.Unreadable.Error()
	}
	lacks := make([]string, 0, len(m.Frameworks))
	for _, framework := range m.Frameworks {
		lacks = append(lacks, detectorOf(framework).lacks)
	}
	if m.Manifest == "" {
		return english.And(lacks)
	}
	return "a " + m.Manifest + " with " + english.And(lacks)
}

type UndetectedAppError struct {
	Place   string
	Matches []Detection
	Misses  []Miss
}

func (e *UndetectedAppError) Ambiguous() bool { return len(e.Matches) > 1 }

func (e *UndetectedAppError) HasManifest() bool { return len(e.Matches) > 0 || len(e.Misses) > 0 }

func (e *UndetectedAppError) HasUnreadableManifest() bool {
	return slices.ContainsFunc(e.Misses, func(m Miss) bool { return m.Unreadable != nil })
}

func (e *UndetectedAppError) Languages() []Language {
	var languages []Language
	add := func(l Language) {
		if !slices.Contains(languages, l) {
			languages = append(languages, l)
		}
	}
	for _, m := range e.Matches {
		add(m.Language)
	}
	for _, m := range e.Misses {
		add(m.Language)
	}
	return languages
}

func (e *UndetectedAppError) ManifestsOf(written Language) []string {
	var names []string
	add := func(l Language, name string) {
		if l == written && name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for _, m := range e.Matches {
		add(m.Language, m.Manifest)
	}
	for _, m := range e.Misses {
		add(m.Language, m.Manifest)
	}
	return names
}

func (e *UndetectedAppError) Language() Language {
	if languages := e.Languages(); len(languages) == 1 {
		return languages[0]
	}
	return ""
}

func (e *UndetectedAppError) GoModule() *gomodule.Module {
	if e.Language() != Go {
		return nil
	}
	for _, m := range e.Matches {
		if m.GoModule != nil {
			return m.GoModule
		}
	}
	for _, m := range e.Misses {
		if m.GoModule != nil {
			return m.GoModule
		}
	}
	return nil
}

func (e *UndetectedAppError) Error() string {
	if e.Ambiguous() {
		holdings := make([]string, 0, len(e.Matches))
		for _, m := range e.Matches {
			holdings = append(holdings, detectorOf(m.Framework).label+" ("+m.Evidence+")")
		}
		if len(holdings) == 2 {
			return e.Place + " holds both " + english.And(holdings)
		}
		return e.Place + " holds " + english.And(holdings)
	}
	if len(e.Misses) == 0 {
		return e.Place + " holds none of " + english.Or(ManifestNames())
	}
	described := make([]string, 0, len(e.Misses))
	for _, m := range e.Misses {
		described = append(described, m.describe())
	}
	return e.Place + " holds " + english.And(described)
}

func (m *manifest) find(dir, root string) (opened, bool, error) {
	if m.open != nil {
		return m.open(dir, root)
	}
	for _, name := range m.files {
		path := filepath.Join(dir, name)
		if !isRegularFile(path) {
			continue
		}
		found := opened{name: name}
		if m.read == nil {
			return found, true, nil
		}
		read, err := m.read(path)
		if err != nil {
			return found, true, err
		}
		found.contents = read
		return found, true, nil
	}
	return opened{}, false, nil
}

func openGoModule(dir, root string) (opened, bool, error) {
	module, present, err := gomodule.FindInProject(dir, root)
	found := opened{name: gomodule.File}
	if err != nil || !present {
		return found, present, err
	}
	found.module = &module
	return found, true, nil
}

func hasGoModuleOrSource(dir string) bool {
	if isRegularFile(filepath.Join(dir, gomodule.File)) {
		return true
	}
	return slices.ContainsFunc(goEntrypoints, func(candidate string) bool {
		entries, _ := os.ReadDir(filepath.Join(dir, filepath.FromSlash(candidate)))
		return slices.ContainsFunc(entries, func(entry os.DirEntry) bool {
			return !entry.IsDir() && filepath.Ext(entry.Name()) == ".go"
		})
	})
}

func readPackage(path string) (contents, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return contents{}, fmt.Errorf("read %s: %w", path, err)
	}
	var manifest struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return contents{}, fmt.Errorf("%s is not JSON: %w", path, err)
	}
	dependencies := map[string]bool{}
	for name := range manifest.Dependencies {
		dependencies[name] = true
	}
	for name := range manifest.DevDependencies {
		dependencies[name] = true
	}
	return contents{dependencies: dependencies}, nil
}

func readCrate(path string) (contents, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return contents{}, fmt.Errorf("read %s: %w", path, err)
	}
	var crate crateManifest
	if err := toml.Unmarshal(body, &crate); err != nil {
		return contents{}, fmt.Errorf("%s is not TOML: %w", path, err)
	}
	return contents{crate: crate}, nil
}

func matchNext(dir string, c contents) (string, string) {
	if c.dependencies[nextDependency] {
		return nodeManifest + " depends on " + nextDependency, ""
	}
	for _, name := range nextConfigNames {
		if isRegularFile(filepath.Join(dir, name)) {
			return name, ""
		}
	}
	return "", ""
}

func matchSvelteKit(_ string, c contents) (string, string) {
	if c.dependencies[svelteKitDependency] {
		return nodeManifest + " depends on " + svelteKitDependency, ""
	}
	return "", ""
}

func matchNestJS(_ string, c contents) (string, string) {
	if c.dependencies[nestDependency] {
		return nodeManifest + " depends on " + nestDependency, ""
	}
	return "", ""
}

func firstFile(candidates []string) func(string, contents) (string, string) {
	return func(dir string, _ contents) (string, string) {
		for _, candidate := range candidates {
			if isRegularFile(filepath.Join(dir, filepath.FromSlash(candidate))) {
				return candidate, candidate
			}
		}
		return "", ""
	}
}

func matchGo(dir string, _ contents) (string, string) {
	for _, candidate := range goEntrypoints {
		if !isMainPackage(filepath.Join(dir, filepath.FromSlash(candidate))) {
			continue
		}
		if candidate == "." {
			return "package main", candidate
		}
		return "package main in " + candidate, candidate
	}
	return "", ""
}

func isMainPackage(dir string) bool {
	ctx := build.Default
	ctx.GOOS = goBuildOS
	ctx.CgoEnabled = false
	pkg, err := ctx.ImportDir(dir, 0)
	return err == nil && pkg.Name == "main" && len(pkg.GoFiles) > 0
}

func matchRust(dir string, c contents) (string, string) {
	if c.crate.Package == nil {
		return "", ""
	}
	if isRegularFile(filepath.Join(dir, "src", "main.rs")) {
		return "src/main.rs", ""
	}
	if len(c.crate.Bins) > 0 {
		return "a [[bin]] target", ""
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "src", "bin"))
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case !entry.IsDir() && filepath.Ext(name) == ".rs":
			return "src/bin/" + name, ""
		case entry.IsDir() && isRegularFile(filepath.Join(dir, "src", "bin", name, "main.rs")):
			return "src/bin/" + name + "/main.rs", ""
		}
	}
	return "", ""
}
