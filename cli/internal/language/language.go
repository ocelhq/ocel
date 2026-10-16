package language

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
	written, found, err := byPrecedence(dir, "")
	if err != nil {
		return Manifested(dir)[0], true
	}
	return written, found
}

func byPrecedence(dir, entrypoint string) (Language, bool, error) {
	written, found, err := Detect(dir, entrypoint)
	var ambiguity *AmbiguityError
	if errors.As(err, &ambiguity) {
		return ambiguity.Languages[0], true, nil
	}
	return written, found, err
}

func ofFramework(framework string) (Language, bool) {
	for _, f := range frameworks {
		if f.name == framework {
			return f.language, true
		}
	}
	return "", false
}

func DetectFramework(dir, entrypoint string) (framework string, found bool, err error) {
	written, found, err := byPrecedence(dir, entrypoint)
	if err != nil || !found {
		return "", false, err
	}
	return FrameworkOf(dir, written)
}

func FrameworkOf(dir string, written Language) (framework string, found bool, err error) {
	if written == JS {
		framework, _, err := readNodeApp(dir)
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

type nodePackage struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Scripts         map[string]string `json:"scripts"`
	Main            string            `json:"main"`
}

func readNodeManifest(dir string) (nodePackage, error) {
	path := filepath.Join(dir, nodeManifest)
	body, err := os.ReadFile(path)
	if err != nil {
		return nodePackage{}, fmt.Errorf("read %s: %w", path, err)
	}
	var manifest nodePackage
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nodePackage{}, fmt.Errorf("%s is not JSON: %w", path, err)
	}
	return manifest, nil
}

func readNodeApp(dir string) (string, nodePackage, error) {
	if slices.ContainsFunc(nextConfigNames, func(name string) bool { return isRegularFile(filepath.Join(dir, name)) }) {
		return buildoutput.FrameworkNext, nodePackage{}, nil
	}
	manifest, err := readNodeManifest(dir)
	if err != nil {
		return "", nodePackage{}, err
	}
	return manifest.framework(), manifest, nil
}

func (m nodePackage) dependsOn(dependency string) bool {
	_, runtime := m.Dependencies[dependency]
	_, development := m.DevDependencies[dependency]
	return runtime || development
}

func (m nodePackage) framework() string {
	if m.dependsOn(nextDependency) {
		return buildoutput.FrameworkNext
	}
	if m.dependsOn(svelteKitDependency) {
		return buildoutput.FrameworkSvelteKit
	}
	return ""
}

func (m nodePackage) served(dir string) (string, bool) {
	if entrypoint, ok := NodeEntrypoint(dir); ok {
		return entrypoint, true
	}
	if strings.TrimSpace(m.Scripts["start"]) != "" {
		return "start script", true
	}
	if m.Main != "" && (isRegularFile(filepath.Join(dir, filepath.FromSlash(m.Main))) || strings.TrimSpace(m.Scripts["build"]) != "") {
		return "main " + m.Main, true
	}
	for _, index := range nodeIndexes {
		if isRegularFile(filepath.Join(dir, index)) {
			return index, true
		}
	}
	return "", false
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
