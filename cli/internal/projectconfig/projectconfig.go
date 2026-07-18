// Package projectconfig locates, transpiles, and executes a user's
// ocel.config.ts to resolve their project's configuration.
package projectconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// ConfigFileName is the name of the file Resolve looks for.
const ConfigFileName = "ocel.config.ts"

// buildDirName is the Ocel-internal build-artifact folder written next to
// the resolved config. It must stay gitignored.
const buildDirName = ".ocel"

// initHint is appended to every resolution failure so the user knows how to
// fix it.
const initHint = "run `ocel init` to create one"

// defaultDiscoveryPaths is used for discovery.paths when the config omits it.
var defaultDiscoveryPaths = []string{"ocel"}

// Discovery controls where the CLI looks for resource declarations.
type Discovery struct {
	Paths []string
}

// ProviderDescriptor identifies the deploy target a config's `provider`
// field names — e.g. `provider: awsProvider({...})` in ocel.config.ts,
// exported by packages like @ocel/provider-aws (see
// packages/provider-aws/src/index.ts). Package is used to locate the
// provider's binary; Options is forwarded to it opaquely (the CLI never
// inspects it) and is always well-formed JSON, `{}` when the user passed
// none.
type ProviderDescriptor struct {
	Package string
	Options json.RawMessage
}

// App is a resolved, defaulted application declared in ocel.config.ts.
type App struct {
	Name string
	// Path is the app's directory, relative to the config dir.
	Path string
	// Framework is the app's web framework, passed through to the builder.
	// Empty means the builder auto-detects it. The builder validates the value.
	Framework string
	// Entrypoint is an optional override relative to Path.
	Entrypoint string
	// Domains maps a lowercased environment class ("production") to the custom
	// hostname this app is served on, mirroring Config.Domains. Empty entries
	// are dropped.
	Domains map[string]string
	// Compute is Ocel-internal: it defaults to "serverless" during
	// normalization, is never user-settable, and is never serialized onto
	// the manifest wire.
	Compute string
}

// Config is the resolved, defaulted project configuration read from
// ocel.config.ts.
type Config struct {
	ProjectID string
	Discovery Discovery
	// Provider is nil when the config has no `provider` field configured.
	Provider *ProviderDescriptor
	// Apps holds the normalized applications declared in the config.
	Apps []App
	// Domains maps a lowercased environment class ("production") to the custom
	// hostname the web-facing worker is served on. Empty entries are dropped.
	Domains map[string]string
	// Dir is the directory containing the resolved ocel.config.ts.
	// discovery.paths are relative to it.
	Dir string
}

// RequireProvider returns c.Provider, or a clear error naming what to add to
// ocel.config.ts if it's absent. Callers (e.g. `ocel deploy`) should call
// this before spawning anything provider-related.
func (c *Config) RequireProvider() (*ProviderDescriptor, error) {
	if c.Provider == nil {
		return nil, fmt.Errorf("no provider configured in %s — add `provider: awsProvider({...})` (from @ocel/provider-aws) to your config", ConfigFileName)
	}
	return c.Provider, nil
}

// rawConfig mirrors the JSON shape emitted by executing the user's bundled
// ocel.config.ts.
type rawConfig struct {
	ProjectID string `json:"projectId"`
	Discovery struct {
		Paths []string `json:"paths"`
	} `json:"discovery"`
	Provider *struct {
		Package string          `json:"package"`
		Options json.RawMessage `json:"options"`
	} `json:"provider"`
	Apps []struct {
		Name       string     `json:"name"`
		Path       string     `json:"path"`
		Framework  string     `json:"framework"`
		Entrypoint string     `json:"entrypoint"`
		Domains    rawDomains `json:"domains"`
	} `json:"apps"`
	Domains rawDomains `json:"domains"`
}

// rawDomains is the class-keyed domain block, shared by the project and each
// app. Only "production" is settable today.
type rawDomains struct {
	Production string `json:"production"`
}

// normalizeDomains lowers a raw domain block into the class-keyed map the
// manifest carries, dropping empty entries.
func normalizeDomains(raw rawDomains) map[string]string {
	domains := map[string]string{}
	if raw.Production != "" {
		domains["production"] = strings.ToLower(raw.Production)
	}
	return domains
}

// defaultCompute is the Ocel-internal compute target applied to every app
// during normalization. It is not user-settable.
const defaultCompute = "serverless"

// Resolve walks up from startDir to find the nearest ancestor
// ocel.config.ts, bundles and executes it, and returns its parsed,
// defaulted configuration.
//
// If no config is found, it can't be bundled/executed, or it doesn't emit a
// projectId, the returned error's message instructs the user to run
// `ocel init`.
func Resolve(startDir string) (*Config, error) {
	configPath, err := findConfigFile(startDir)
	if err != nil {
		return nil, fmt.Errorf("no %s found in %s or any parent directory — %s", ConfigFileName, startDir, initHint)
	}

	output, err := buildAndRun(configPath)
	if err != nil {
		return nil, fmt.Errorf("could not read %s: %w — %s", configPath, err, initHint)
	}

	var raw rawConfig
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("%s did not emit valid configuration: %w — %s", configPath, err, initHint)
	}
	if raw.ProjectID == "" {
		return nil, fmt.Errorf("%s is missing required \"projectId\" — %s", configPath, initHint)
	}

	paths := raw.Discovery.Paths
	if len(paths) == 0 {
		paths = defaultDiscoveryPaths
	}

	var provider *ProviderDescriptor
	if raw.Provider != nil {
		options := raw.Provider.Options

		if len(options) == 0 || string(options) == "null" {
			options = json.RawMessage("{}")
		}

		provider = &ProviderDescriptor{Package: raw.Provider.Package, Options: options}
	}

	apps, err := normalizeApps(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}

	domains := normalizeDomains(raw.Domains)

	return &Config{
		ProjectID: raw.ProjectID,
		Discovery: Discovery{Paths: paths},
		Provider:  provider,
		Apps:      apps,
		Domains:   domains,
		Dir:       filepath.Dir(configPath),
	}, nil
}

// validAppName reports whether a name is usable as an app's identity. Build
// output is namespaced per app by directory, so the only constraint is that the
// name stay a single path segment inside the output tree: anything that is a
// separator, climbs out, or roots elsewhere is rejected. Everything else — dots
// included — is harmless in a directory name and stays allowed.
func validAppName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\`) || filepath.IsAbs(name) {
		return false
	}
	return true
}

// normalizeApps validates the raw apps and applies internal defaults. It is
// framework-agnostic structural work only: it checks names and paths and sets
// the Ocel-internal compute target. Framework validation and detection are the
// node builder's job — the framework string is passed through untouched.
func normalizeApps(raw rawConfig) ([]App, error) {
	if len(raw.Apps) == 0 {
		return nil, nil
	}

	apps := make([]App, 0, len(raw.Apps))
	seen := make(map[string]bool, len(raw.Apps))
	for _, a := range raw.Apps {
		if a.Name == "" {
			return nil, fmt.Errorf("app is missing required \"name\"")
		}
		if !validAppName(a.Name) {
			return nil, fmt.Errorf("invalid app name %q — an app name is one directory segment of the build output, so it may not be a path separator, \"..\", or an absolute path", a.Name)
		}
		if seen[a.Name] {
			return nil, fmt.Errorf("duplicate app name %q — app names must be unique", a.Name)
		}
		seen[a.Name] = true

		if a.Path == "" {
			return nil, fmt.Errorf("app %q is missing required \"path\"", a.Name)
		}

		apps = append(apps, App{
			Name:       a.Name,
			Path:       a.Path,
			Framework:  a.Framework,
			Entrypoint: a.Entrypoint,
			Domains:    normalizeDomains(a.Domains),
			Compute:    defaultCompute,
		})
	}

	return apps, nil
}

// buildAndRun bundles configPath (and a small wrapper that JSON-serializes
// its default export) with esbuild's Go API, writes the result under
// .ocel/ next to the config, executes it with the user's node, and returns
// what it wrote to stdout.
func buildAndRun(configPath string) ([]byte, error) {
	dir := filepath.Dir(configPath)
	outDir := filepath.Join(dir, buildDirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", buildDirName, err)
	}
	outfile := filepath.Join(outDir, "config.mjs")

	entry := fmt.Sprintf("import config from %q;\nprocess.stdout.write(JSON.stringify(config));\n", configPath)

	result := api.Build(api.BuildOptions{
		Stdin: &api.StdinOptions{
			Contents:   entry,
			ResolveDir: dir,
			Sourcefile: "ocel-config-entry.ts",
			Loader:     api.LoaderTS,
		},
		Bundle:   true,
		Platform: api.PlatformNode,
		Format:   api.FormatESModule,
		Outfile:  outfile,
		Write:    true,
	})
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		return nil, fmt.Errorf("bundle failed:\n%s", strings.Join(msgs, "\n"))
	}

	if _, err := exec.LookPath("node"); err != nil {
		return nil, fmt.Errorf("node not found on PATH: %w", err)
	}

	cmd := exec.Command("node", outfile)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("node exited with error: %s", strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("run node: %w", err)
	}

	return stdout, nil
}

// findConfigFile walks up from startDir (tsconfig-style) looking for the
// nearest ancestor ConfigFileName. It returns os.ErrNotExist if none is
// found by the time it reaches the filesystem root.
func findConfigFile(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for {
		candidate := filepath.Join(dir, ConfigFileName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
