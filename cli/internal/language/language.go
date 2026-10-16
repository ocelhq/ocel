package language

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

type Language string

const (
	JS     Language = "js"
	Go     Language = "go"
	Python Language = "python"
	Rust   Language = "rust"
)

var manifests = []struct {
	name     string
	language Language
}{
	{"go.mod", Go},
	{"pyproject.toml", Python},
	{"requirements.txt", Python},
	{nodeManifest, JS},
	{"Cargo.toml", Rust},
}

var extendedByRust = []Language{JS, Python}

var frameworks = []struct {
	name     string
	language Language
}{
	{buildoutput.FrameworkNode, JS},
	{buildoutput.FrameworkNext, JS},
	{buildoutput.FrameworkSvelteKit, JS},
	{buildoutput.FrameworkGo, Go},
	{buildoutput.FrameworkPython, Python},
	{buildoutput.FrameworkRust, Rust},
}

const nodeManifest = "package.json"

const nextDependency = "next"

const svelteKitDependency = "@sveltejs/kit"

var nextConfigNames = []string{"next.config.js", "next.config.mjs", "next.config.ts"}

func ManifestNames() []string {
	names := make([]string, 0, len(manifests))
	for _, m := range manifests {
		names = append(names, m.name)
	}
	return names
}

func Manifested(dir string) []Language {
	var found []Language
	for _, m := range manifests {
		if slices.Contains(found, m.language) || !isRegularFile(filepath.Join(dir, m.name)) {
			continue
		}
		if m.language == Rust && slices.ContainsFunc(found, func(l Language) bool { return slices.Contains(extendedByRust, l) }) {
			continue
		}
		found = append(found, m.language)
	}
	return found
}

func Of(dir string) (Language, bool) {
	found := Manifested(dir)
	if len(found) == 0 {
		return "", false
	}
	if apps := holdingApps(dir, found); len(apps) > 0 {
		return apps[0], true
	}
	return found[0], true
}

func Detect(dir string) (Language, bool, error) {
	found := Manifested(dir)
	if len(found) == 0 {
		return "", false, nil
	}
	if len(found) == 1 {
		return found[0], true, nil
	}
	if apps := holdingApps(dir, found); len(apps) == 1 {
		return apps[0], true, nil
	}
	holdings := make([]string, 0, len(found))
	for _, l := range found {
		holdings = append(holdings, holding(dir, l))
	}
	return "", false, &AmbiguityError{Dir: dir, Languages: found, holdings: holdings}
}

type AmbiguityError struct {
	Dir       string
	Languages []Language
	holdings  []string
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

const pythonEntry = "main.py"

func holdingApps(dir string, found []Language) []Language {
	if len(found) < 2 {
		return found
	}
	var apps []Language
	for _, l := range found {
		if holdsApp(dir, l) {
			apps = append(apps, l)
		}
	}
	return apps
}

func holdsApp(dir string, l Language) bool {
	switch l {
	case JS:
		framework, _ := jsFramework(dir)
		return framework != ""
	case Python:
		return isRegularFile(filepath.Join(dir, pythonEntry))
	default:
		return true
	}
}

func holding(dir string, l Language) string {
	framework := ""
	if l == JS {
		framework, _ = jsFramework(dir)
	}
	switch {
	case framework == buildoutput.FrameworkNext:
		return "a Next.js app"
	case framework == buildoutput.FrameworkSvelteKit:
		return "a SvelteKit app"
	case l == Python && holdsApp(dir, l):
		return "a python app (" + pythonEntry + ")"
	}
	for _, m := range manifests {
		if m.language == l && isRegularFile(filepath.Join(dir, m.name)) {
			return "a " + m.name
		}
	}
	return "a " + string(l) + " project"
}

func jsFramework(dir string) (string, error) {
	next, err := isNextApp(dir)
	if err != nil {
		return "", err
	}
	if next {
		return buildoutput.FrameworkNext, nil
	}
	svelteKit, err := dependsOn(dir, svelteKitDependency)
	if err != nil || !svelteKit {
		return "", err
	}
	return buildoutput.FrameworkSvelteKit, nil
}

func ofFramework(framework string) (Language, bool) {
	for _, f := range frameworks {
		if f.name == framework {
			return f.language, true
		}
	}
	return "", false
}

func DetectFramework(dir string) (framework string, found bool, err error) {
	written, ok := Of(dir)
	if !ok {
		return "", false, nil
	}
	if written == JS {
		framework, err := jsFramework(dir)
		if err != nil {
			return "", false, err
		}
		if framework != "" {
			return framework, true, nil
		}
	}
	for _, f := range frameworks {
		if f.language == written {
			return f.name, true, nil
		}
	}
	return "", false, nil
}

func isNextApp(dir string) (bool, error) {
	for _, name := range nextConfigNames {
		if isRegularFile(filepath.Join(dir, name)) {
			return true, nil
		}
	}
	return dependsOn(dir, nextDependency)
}

func dependsOn(dir, dependency string) (bool, error) {
	path := filepath.Join(dir, nodeManifest)
	body, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	var manifest struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return false, fmt.Errorf("%s is not JSON: %w", path, err)
	}
	_, runtime := manifest.Dependencies[dependency]
	_, development := manifest.DevDependencies[dependency]
	return runtime || development, nil
}

func OfApp(framework, dir string) Language {
	if language, ok := ofFramework(framework); ok {
		return language
	}
	if language, ok := Of(dir); ok {
		return language
	}
	return JS
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
