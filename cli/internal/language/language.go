package language

import (
	"encoding/json"
	"fmt"
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
	{buildoutput.FrameworkGo, Go},
	{buildoutput.FrameworkPython, Python},
	{buildoutput.FrameworkRust, Rust},
}

const nodeManifest = "package.json"

const nextDependency = "next"

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
	return found[0], true
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
		next, err := isNextApp(dir)
		if err != nil {
			return "", false, err
		}
		if next {
			return buildoutput.FrameworkNext, true, nil
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
	_, dependency := manifest.Dependencies[nextDependency]
	_, devDependency := manifest.DevDependencies[nextDependency]
	return dependency || devDependency, nil
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
