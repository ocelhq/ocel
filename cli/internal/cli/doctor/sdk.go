package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/modfile"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/sdkversion"
)

type declaredSDK struct {
	language string
	manifest string
	spec     string
}

func sdkDirs(cfg *projectconfig.Config) []string {
	dirs := []string{filepath.Clean(cfg.Dir)}
	for _, app := range cfg.Apps {
		dir := filepath.Join(cfg.Dir, app.Path)
		if !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func declaredSDKs(cfg *projectconfig.Config) []declaredSDK {
	var found []declaredSDK
	for _, dir := range sdkDirs(cfg) {
		for _, read := range []func(string) (declaredSDK, bool){npmSDK, pythonSDK, rustSDK, goSDK} {
			if sdk, ok := read(dir); ok {
				found = append(found, sdk)
			}
		}
	}
	return found
}

func npmSDK(dir string) (declaredSDK, bool) {
	path := filepath.Join(dir, "package.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return declaredSDK{}, false
	}
	var manifest struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(raw, &manifest) != nil {
		return declaredSDK{}, false
	}
	for _, deps := range []map[string]string{manifest.Dependencies, manifest.DevDependencies} {
		if spec, ok := deps["ocel"]; ok {
			return declaredSDK{language: sdkversion.JS, manifest: path, spec: spec}, true
		}
	}
	return declaredSDK{}, false
}

var pythonRequirement = regexp.MustCompile(`^ocel\s*(?:\[[^\]]*\])?\s*([^;]*)`)

func pythonSDK(dir string) (declaredSDK, bool) {
	path := filepath.Join(dir, "pyproject.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return declaredSDK{}, false
	}
	var project struct {
		Project struct {
			Dependencies []string `toml:"dependencies"`
		} `toml:"project"`
	}
	if toml.Unmarshal(raw, &project) != nil {
		return declaredSDK{}, false
	}
	for _, requirement := range project.Project.Dependencies {
		match := pythonRequirement.FindStringSubmatch(strings.TrimSpace(requirement))
		if match == nil {
			continue
		}
		spec := strings.TrimSpace(match[1])
		if spec != "" && !strings.ContainsAny(spec[:1], "=<>!~@") {
			continue
		}
		return declaredSDK{language: sdkversion.Python, manifest: path, spec: spec}, true
	}
	return declaredSDK{}, false
}

func rustSDK(dir string) (declaredSDK, bool) {
	path := filepath.Join(dir, "Cargo.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return declaredSDK{}, false
	}
	var crate struct {
		Dependencies map[string]any `toml:"dependencies"`
		Target       map[string]struct {
			Dependencies map[string]any `toml:"dependencies"`
		} `toml:"target"`
	}
	if toml.Unmarshal(raw, &crate) != nil {
		return declaredSDK{}, false
	}
	tables := []map[string]any{crate.Dependencies}
	for _, target := range crate.Target {
		tables = append(tables, target.Dependencies)
	}
	for _, deps := range tables {
		for name, spec := range deps {
			if held, ok := crateSpec(name, spec); ok {
				return declaredSDK{language: sdkversion.Rust, manifest: path, spec: held}, true
			}
		}
	}
	return declaredSDK{}, false
}

func crateSpec(name string, spec any) (string, bool) {
	switch held := spec.(type) {
	case string:
		return held, name == "ocel-sdk"
	case map[string]any:
		if renamed, ok := held["package"].(string); ok {
			name = renamed
		}
		if name != "ocel-sdk" {
			return "", false
		}
		version, _ := held["version"].(string)
		return version, true
	}
	return "", false
}

func goSDK(dir string) (declaredSDK, bool) {
	path := filepath.Join(dir, "go.mod")
	raw, err := os.ReadFile(path)
	if err != nil {
		return declaredSDK{}, false
	}
	parsed, err := modfile.ParseLax(path, raw, nil)
	if err != nil {
		return declaredSDK{}, false
	}
	for _, required := range parsed.Require {
		if required.Mod.Path == "ocel.dev" {
			return declaredSDK{language: sdkversion.Go, manifest: path, spec: required.Mod.Version}, true
		}
	}
	return declaredSDK{}, false
}

var pinnedVersion = regexp.MustCompile(`\d+\.\d+\.\d+(?:-rc\.\d+|rc\d+|-0\.nightly\.\d{8}\.g[0-9a-f]{7}|\.dev\d{8})?`)

func sdkChecks(cfg *projectconfig.Config, cli string) []check {
	var checks []check
	for _, sdk := range declaredSDKs(cfg) {
		checks = append(checks, sdkCheck(cfg.Dir, sdk, cli))
	}
	return checks
}

func sdkCheck(root string, sdk declaredSDK, cli string) check {
	manifest, err := filepath.Rel(root, sdk.manifest)
	if err != nil {
		manifest = sdk.manifest
	}
	spec := sdk.spec
	if spec == "" {
		spec = "unpinned"
	}
	named := sdkversion.Name(sdk.language) + " " + spec + " in " + filepath.ToSlash(manifest)

	pinned := pinnedVersion.FindString(sdk.spec)
	switch {
	case !sdkversion.Released(cli):
		return check{verdict: verdictNeutral, text: named + " — not compared with a development build of the CLI"}
	case !sdkversion.Released(pinned):
		return check{verdict: verdictNeutral, text: named + " — names no release to compare with this CLI"}
	case sdkversion.Compatible(cli, pinned):
		return check{verdict: verdictPass, text: named + " — works with this CLI " + cli}
	}
	return check{
		verdict: verdictFail,
		text:    named + " — this CLI is " + cli + ", and an SDK works with the CLI of its own release",
		fix:     "run `" + sdkversion.Upgrade(sdk.language, cli) + "`",
	}
}
