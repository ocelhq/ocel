// Package projectconfig locates, transpiles, and executes a user's
// ocel.config.ts to resolve their project's configuration.
package projectconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// ConfigFileName is the name of the file Resolve looks for.
const ConfigFileName = "ocel.config.ts"

// scratchDirName is the Ocel-internal folder at the project root: it holds
// build artifacts and the cloud link record, and it anchors the root walk
// alongside the config. It must stay gitignored.
const scratchDirName = ".ocel"

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
	// hostnames this app is served on, mirroring Config.Domains. Empty entries
	// are dropped.
	Domains map[string][]string
	// Compute is Ocel-internal: it defaults to "serverless" during
	// normalization, is never user-settable, and is never serialized onto
	// the manifest wire.
	Compute string
}

// Config is the resolved, defaulted project configuration read from
// ocel.config.ts.
type Config struct {
	// Slug is the project's stable, human-authored deployment identity: it keys
	// the project's own instance in the shared deployments-store worker.
	Slug      string
	Discovery Discovery
	// Provider is nil when the config has no `provider` field configured.
	Provider *ProviderDescriptor
	// Apps holds the normalized applications declared in the config.
	Apps []App
	// Domains maps a lowercased environment class ("production") to the custom
	// hostnames the web-facing worker is served on. Empty entries are dropped.
	Domains map[string][]string
	// Dir is the project root — the directory holding the resolved
	// ocel.config.ts, or the root findProjectRoot settled on when there is no
	// config. discovery.paths are relative to it.
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
	Slug      string `json:"slug"`
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
// app. "production" is one or more plain hostnames (or "*." wildcards for a
// multitenant app), each attached as a worker route; "preview" is a single
// wildcard the ephemeral and persistent previews are served under, one
// subdomain label per pointer.
type rawDomains struct {
	Production stringOrList `json:"production"`
	Preview    string       `json:"preview"`
}

// stringOrList unmarshals a JSON value that may be a single string or an array
// of strings into a slice — the authoring ergonomics for production domains,
// where `production: "app.com"` and `production: ["app.com", "www.app.com"]`
// are both valid. A missing/null/empty value is the empty slice.
type stringOrList []string

func (s *stringOrList) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*s = nil
		return nil
	}
	if trimmed[0] == '[' {
		var list []string
		if err := json.Unmarshal(b, &list); err != nil {
			return err
		}
		*s = list
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	if one == "" {
		*s = nil
		return nil
	}
	*s = []string{one}
	return nil
}

// normalizeDomains lowers a raw domain block into the class-keyed lists the
// manifest carries: dropping empty entries, validating the preview wildcard,
// deduping the production hostnames, and rejecting a production hostname that
// exactly equals the preview wildcard (production and preview cannot attach the
// same worker-route pattern).
func normalizeDomains(raw rawDomains) (map[string][]string, error) {
	domains := map[string][]string{}

	var preview string
	if raw.Preview != "" {
		preview = strings.ToLower(raw.Preview)
		if err := validatePreviewDomain(preview); err != nil {
			return nil, err
		}
		domains["preview"] = []string{preview}
	}

	production, err := normalizeProductionDomains(raw.Production, preview)
	if err != nil {
		return nil, err
	}
	if len(production) > 0 {
		domains["production"] = production
	}

	return domains, nil
}

// normalizeProductionDomains lowercases, trims and dedupes the production
// hostnames in declared order, and rejects any that equals the preview wildcard.
func normalizeProductionDomains(raw stringOrList, preview string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, d := range raw {
		host := strings.ToLower(strings.TrimSpace(d))
		if host == "" {
			continue
		}
		if preview != "" && host == preview {
			return nil, fmt.Errorf("production domain %q is identical to the preview wildcard %q; production and preview cannot attach the same worker-route pattern — give them different hostnames", host, preview)
		}
		if seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	return out, nil
}

// validatePreviewDomain enforces that a preview domain is a single-wildcard
// pattern whose sole wildcard is the leftmost label — e.g. "*.preview.app.com".
// The subdomain label the wildcard stands in for is the store pointer; a
// pattern with no wildcard, a wildcard that isn't a whole leftmost label, or
// more than one wildcard cannot host per-pointer subdomains and is rejected.
func validatePreviewDomain(domain string) error {
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return fmt.Errorf("preview domain %q must be a wildcard hostname like \"*.preview.example.com\"", domain)
	}
	if strings.Count(domain, "*") != 1 {
		return fmt.Errorf("preview domain %q must contain exactly one wildcard, in its leftmost label (e.g. \"*.preview.example.com\")", domain)
	}
	if labels[0] != "*" {
		return fmt.Errorf("preview domain %q must have its wildcard as the whole leftmost label (e.g. \"*.preview.example.com\")", domain)
	}
	return nil
}

// PreviewBaseDomain derives the base domain the preview wildcard is anchored on
// by stripping the leading "*." — "*.preview.app.com" becomes "preview.app.com".
// It is the OCEL_PREVIEW_BASE_DOMAIN the frozen preview worker matches request
// subdomains against. Returns "" for a non-wildcard or empty input.
func PreviewBaseDomain(previewDomain string) string {
	if !strings.HasPrefix(previewDomain, "*.") {
		return ""
	}
	return previewDomain[len("*."):]
}

// defaultCompute is the Ocel-internal compute target applied to every app
// during normalization. It is not user-settable.
const defaultCompute = "serverless"

// Resolve finds the project root, bundles and executes the ocel.config.ts it
// holds, and returns the parsed, defaulted configuration. It is the entry
// point for every command that needs a real config — deploy, preview,
// bootstrap and friends — and fails when there is none.
//
// If no config is found, or it can't be bundled/executed, the returned error's
// message instructs the user to run `ocel init`.
func Resolve(startDir string) (*Config, error) {
	root, err := findProjectRoot(startDir)
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(root, ConfigFileName)
	if !isFile(configPath) {
		return nil, fmt.Errorf("no %s found in %s or any parent directory — %s", ConfigFileName, startDir, initHint)
	}
	return load(configPath)
}

// ResolveOptional is Resolve for commands that only need the project root and
// discovery.paths — `ocel dev` and `ocel run`. Apps, domains, provider and slug
// are all deploy-only, so an absent config is not an error here: it yields a
// defaulted configuration rooted at the project root. A config that exists is
// still parsed and validated in full, so a broken one is never silently
// ignored.
func ResolveOptional(startDir string) (*Config, error) {
	root, err := findProjectRoot(startDir)
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(root, ConfigFileName)
	if !isFile(configPath) {
		return &Config{
			Discovery: Discovery{Paths: defaultDiscoveryPaths},
			Dir:       root,
		}, nil
	}
	return load(configPath)
}

// load bundles, executes and normalizes the config at configPath.
func load(configPath string) (*Config, error) {
	output, err := buildAndRun(configPath)
	if err != nil {
		return nil, fmt.Errorf("could not read %s: %w — %s", configPath, err, initHint)
	}

	var raw rawConfig
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("%s did not emit valid configuration: %w — %s", configPath, err, initHint)
	}
	if raw.Slug == "" {
		return nil, fmt.Errorf("%s is missing required \"slug\" — %s", configPath, initHint)
	}
	if !validSlug(raw.Slug) {
		return nil, fmt.Errorf("%s has an invalid \"slug\" %q — it must be a DNS label: lowercase letters, digits and hyphens, 1–63 characters, not starting or ending with a hyphen", configPath, raw.Slug)
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

	domains, err := normalizeDomains(raw.Domains)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}

	return &Config{
		Slug:      raw.Slug,
		Discovery: Discovery{Paths: paths},
		Provider:  provider,
		Apps:      apps,
		Domains:   domains,
		Dir:       filepath.Dir(configPath),
	}, nil
}

// slugPattern is the DNS-label shape a project slug must take: it keys the
// project's own instance in the shared deployments-store worker (idFromName), a
// URL path segment and an SSM parameter name, so it stays lowercase, digit and
// hyphen only, 1–63 chars, not hyphen-bounded.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// validSlug reports whether s is a usable project slug.
func validSlug(s string) bool {
	return slugPattern.MatchString(s)
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

		domains, err := normalizeDomains(a.Domains)
		if err != nil {
			return nil, fmt.Errorf("app %q: %w", a.Name, err)
		}
		apps = append(apps, App{
			Name:       a.Name,
			Path:       a.Path,
			Framework:  a.Framework,
			Entrypoint: a.Entrypoint,
			Domains:    domains,
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
	outDir := filepath.Join(dir, scratchDirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", scratchDirName, err)
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

// findProjectRoot walks up from startDir (tsconfig-style) for the nearest
// ancestor holding ocel.config.ts or the .ocel/ scratch dir, falling back to
// startDir itself when it reaches the filesystem root without finding either.
//
// The scratch dir counts because it is what a config-less project leaves
// behind: the first run in a fresh clone anchors at the working directory and
// creates .ocel/ there, so every later run from a subdirectory finds the same
// root.
func findProjectRoot(startDir string) (string, error) {
	start, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for dir := start; ; {
		if isFile(filepath.Join(dir, ConfigFileName)) || isDir(filepath.Join(dir, scratchDirName)) {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return start, nil
		}
		dir = parent
	}
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
