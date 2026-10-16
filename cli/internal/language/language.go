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
	{rustManifest, Rust},
}

const rustManifest = "Cargo.toml"

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

var frameworkLabels = map[string]string{
	buildoutput.FrameworkNext:      "a Next.js app",
	buildoutput.FrameworkSvelteKit: "a SvelteKit app",
}

func ManifestNames() []string {
	names := make([]string, 0, len(manifests))
	for _, m := range manifests {
		names = append(names, m.name)
	}
	return names
}

func Manifested(dir string) []Language {
	found, _ := manifested(dir)
	return found
}

func manifested(dir string) (found []Language, extension bool) {
	for _, m := range manifests {
		if slices.Contains(found, m.language) || !isRegularFile(filepath.Join(dir, m.name)) {
			continue
		}
		if m.language == Rust && slices.ContainsFunc(found, func(l Language) bool { return slices.Contains(extendedByRust, l) }) {
			extension = true
			continue
		}
		found = append(found, m.language)
	}
	return found, extension
}

func Of(dir string) (Language, bool) {
	chosen, found := choose(dir, "")
	return chosen.language, found
}

func ofFramework(framework string) (Language, bool) {
	for _, f := range frameworks {
		if f.name == framework {
			return f.language, true
		}
	}
	return "", false
}

func frameworkOfLanguage(l Language) string {
	for _, f := range frameworks {
		if f.language == l {
			return f.name
		}
	}
	return ""
}

func DetectFramework(dir, entrypoint string) (string, bool, error) {
	chosen, found := choose(dir, entrypoint)
	if chosen.err != nil {
		return "", false, chosen.err
	}
	return chosen.framework, found, nil
}

func nodeFramework(dir string) (string, error) {
	if slices.ContainsFunc(nextConfigNames, func(name string) bool { return isRegularFile(filepath.Join(dir, name)) }) {
		return buildoutput.FrameworkNext, nil
	}
	path := filepath.Join(dir, nodeManifest)
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var manifest struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return "", fmt.Errorf("%s is not JSON: %w", path, err)
	}
	dependsOn := func(dependency string) bool {
		_, runtime := manifest.Dependencies[dependency]
		_, development := manifest.DevDependencies[dependency]
		return runtime || development
	}
	switch {
	case dependsOn(nextDependency):
		return buildoutput.FrameworkNext, nil
	case dependsOn(svelteKitDependency):
		return buildoutput.FrameworkSvelteKit, nil
	}
	return buildoutput.FrameworkNode, nil
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
