package projectconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	nodeManifest       = "package.json"
	goModule           = "go.mod"
	pythonProject      = "pyproject.toml"
	pythonRequirements = "requirements.txt"
	rustManifest       = "Cargo.toml"
	nextDependency     = "next"
)

var nextConfigNames = []string{"next.config.js", "next.config.mjs", "next.config.ts"}

func detectFramework(dir string) (string, error) {
	node := regularFile(filepath.Join(dir, nodeManifest))
	named := make([]string, 0, 3)
	if node {
		named = append(named, providerkit.RuntimeNode)
	}
	if regularFile(filepath.Join(dir, goModule)) {
		named = append(named, providerkit.RuntimeGo)
	}
	if regularFile(filepath.Join(dir, pythonProject)) || regularFile(filepath.Join(dir, pythonRequirements)) {
		named = append(named, providerkit.RuntimePython)
	}
	switch len(named) {
	case 1:
		if !node {
			return named[0], nil
		}
		next, err := nextApp(dir)
		if err != nil {
			return "", err
		}
		if next {
			return providerkit.RuntimeNext, nil
		}
		return providerkit.RuntimeNode, nil
	case 0:
		if regularFile(filepath.Join(dir, rustManifest)) {
			return "", nil
		}
		return "", fmt.Errorf(
			"nothing in %s says what this app is built with: it holds no %s, %s, %s or %s, so set \"framework\" to one of %s",
			dir, nodeManifest, goModule, pythonProject, pythonRequirements, quoted(providerkit.Runtimes()),
		)
	default:
		return "", fmt.Errorf(
			"%s holds the manifests of %s at once, and one app is built one way: set \"framework\" to the one this app is",
			dir, quoted(named),
		)
	}
}

func nextApp(dir string) (bool, error) {
	for _, name := range nextConfigNames {
		if regularFile(filepath.Join(dir, name)) {
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

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func directory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func frameworkOf(app string, dir string, named string, compute string) (string, error) {
	if named != "" {
		if !providerkit.KnownRuntime(named) {
			return "", fmt.Errorf("app %q declares framework %q, which nothing builds: the frameworks are %s", app, named, quoted(providerkit.Runtimes()))
		}
		return named, nil
	}
	if compute == string(providerkit.ComputeContainer) || !directory(dir) {
		return "", nil
	}
	framework, err := detectFramework(dir)
	if err != nil {
		return "", fmt.Errorf("app %q: %w", app, err)
	}
	return framework, nil
}

func architectureOf(app string, arch string) (string, error) {
	if arch == "" || arch == providerkit.ArchX8664 || arch == providerkit.ArchARM64 {
		return arch, nil
	}
	return "", fmt.Errorf("app %q declares arch %q, which names no architecture: the architectures are %q and %q", app, arch, providerkit.ArchX8664, providerkit.ArchARM64)
}

func quoted(values []string) string {
	said := make([]string, 0, len(values))
	for _, value := range values {
		said = append(said, fmt.Sprintf("%q", value))
	}
	if len(said) < 2 {
		return strings.Join(said, "")
	}
	return strings.Join(said[:len(said)-1], ", ") + " and " + said[len(said)-1]
}
