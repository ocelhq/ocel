package language

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

var frameworkDependencies = map[string]string{
	buildoutput.FrameworkNext:      nextDependency,
	buildoutput.FrameworkSvelteKit: svelteKitDependency,
}

const npmAliasPrefix = "npm:"

var runtimes = map[Language]string{JS: "node", Go: "go", Python: "python", Rust: "rust"}

var (
	majorVersion      = regexp.MustCompile(`^[\s^~=<>v]*(\d+)`)
	minorVersion      = regexp.MustCompile(`^[\s^~=<>v]*(\d+\.\d+)`)
	goDirective       = regexp.MustCompile(`(?m)^go\s+(\S+)`)
	requiresPython    = regexp.MustCompile(`(?m)^requires-python\s*=\s*"([^"]*)"`)
	toolchainChannel  = regexp.MustCompile(`(?m)^channel\s*=\s*"([^"]*)"`)
	nodeVersionSource = []string{".nvmrc", ".node-version"}
)

type packageManifest struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Engines         map[string]string `json:"engines"`
}

func DeclaredFrameworkVersion(framework, dir string) string {
	dependency, ok := frameworkDependencies[framework]
	if !ok {
		return ""
	}
	manifest := readPackageManifest(dir)
	declared, ok := manifest.Dependencies[dependency]
	if !ok {
		declared = manifest.DevDependencies[dependency]
	}
	if aliased, ok := strings.CutPrefix(declared, npmAliasPrefix); ok {
		declared = aliased[strings.LastIndex(aliased, "@")+1:]
	}
	return firstMatch(majorVersion, declared)
}

func DeclaredRuntime(language Language, dirs ...string) (runtime, version string) {
	for _, dir := range dirs {
		if version = declaredRuntimeVersion(language, dir); version != "" {
			break
		}
	}
	return runtimes[language], version
}

func declaredRuntimeVersion(language Language, dir string) string {
	switch language {
	case JS:
		if version := firstMatch(majorVersion, readPackageManifest(dir).Engines["node"]); version != "" {
			return version
		}
		for _, name := range nodeVersionSource {
			if version := firstMatch(majorVersion, readText(dir, name)); version != "" {
				return version
			}
		}
	case Go:
		return firstMatch(minorVersion, firstMatch(goDirective, readText(dir, "go.mod")))
	case Python:
		if version := firstMatch(minorVersion, readText(dir, ".python-version")); version != "" {
			return version
		}
		return firstMatch(minorVersion, firstMatch(requiresPython, readText(dir, "pyproject.toml")))
	case Rust:
		if version := firstMatch(minorVersion, firstMatch(toolchainChannel, readText(dir, "rust-toolchain.toml"))); version != "" {
			return version
		}
		return firstMatch(minorVersion, readText(dir, "rust-toolchain"))
	}
	return ""
}

func readPackageManifest(dir string) packageManifest {
	var manifest packageManifest
	_ = json.Unmarshal([]byte(readText(dir, nodeManifest)), &manifest)
	return manifest
}

func readText(dir, name string) string {
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(body)
}

func firstMatch(pattern *regexp.Regexp, text string) string {
	match := pattern.FindStringSubmatch(text)
	if match == nil {
		return ""
	}
	return match[1]
}
