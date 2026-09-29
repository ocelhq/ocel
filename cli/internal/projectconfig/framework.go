package projectconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	nodeManifest   = "package.json"
	nextDependency = "next"
)

var nextConfigNames = []string{"next.config.js", "next.config.mjs", "next.config.ts"}

var languageFrameworks = map[language.Language]string{
	language.JS:     appbuild.FrameworkNode,
	language.Go:     appbuild.FrameworkGo,
	language.Python: appbuild.FrameworkPython,
	language.Rust:   appbuild.FrameworkRust,
}

func detectFramework(dir string) (string, error) {
	found := language.Manifested(dir)
	named := make([]string, 0, len(found))
	for _, written := range found {
		named = append(named, languageFrameworks[written])
	}
	switch len(named) {
	case 1:
		if found[0] != language.JS {
			return named[0], nil
		}
		next, err := IsNextApp(dir)
		if err != nil {
			return "", err
		}
		if next {
			return appbuild.FrameworkNext, nil
		}
		return appbuild.FrameworkNode, nil
	case 0:
		return "", fmt.Errorf(
			"nothing in %s says what this app is built with: it contains no %s, so set \"framework\" to one of %s",
			dir, english.Or(language.ManifestNames()), english.Or(english.Quoted(appbuild.Frameworks())),
		)
	default:
		return "", fmt.Errorf(
			"%s contains the manifests of %s at once, and one app is built one way: set \"framework\" to the one this app is",
			dir, english.And(english.Quoted(named)),
		)
	}
}

func IsNextApp(dir string) (bool, error) {
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
		Dependencies map[string]string `json:"dependencies"`
		DevDeps      map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return false, fmt.Errorf("%s is not JSON: %w", path, err)
	}
	_, dep := manifest.Dependencies[nextDependency]
	_, devDep := manifest.DevDeps[nextDependency]
	return dep || devDep, nil
}

func frameworkOf(app string, dir string, named string, compute string) (string, error) {
	if named != "" {
		if !appbuild.KnownFramework(named) {
			return "", fmt.Errorf("app %q declares framework %q, which nothing builds: the frameworks are %s", app, named, english.And(english.Quoted(appbuild.Frameworks())))
		}
		return named, nil
	}
	if compute == string(provider.ComputeContainer) || !isDir(dir) {
		return "", nil
	}
	framework, err := detectFramework(dir)
	if err != nil {
		return "", fmt.Errorf("app %q: %w", app, err)
	}
	return framework, nil
}

func architectureOf(app string, declared string) (string, error) {
	if declared == "" || declared == arch.X8664 || declared == arch.ARM64 {
		return declared, nil
	}
	return "", fmt.Errorf("app %q declares arch %q, which names no architecture: the architectures are %q and %q", app, declared, arch.X8664, arch.ARM64)
}
