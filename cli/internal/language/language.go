package language

import (
	"os"
	"path/filepath"
	"slices"

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
	{"package.json", JS},
	{"Cargo.toml", Rust},
}

var extendedByRust = []Language{JS, Python}

var frameworks = map[string]Language{
	buildoutput.FrameworkNode:   JS,
	buildoutput.FrameworkNext:   JS,
	buildoutput.FrameworkGo:     Go,
	buildoutput.FrameworkPython: Python,
	buildoutput.FrameworkRust:   Rust,
}

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
	return found[0], true
}

func ofFramework(framework string) (Language, bool) {
	language, ok := frameworks[framework]
	return language, ok
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

func HasClientBundle(framework, dir string) bool {
	if framework != "" {
		return buildoutput.FrameworkBundlesClient(framework)
	}
	language, ok := Of(dir)
	return ok && language == JS
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
