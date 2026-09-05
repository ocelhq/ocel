package projectconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/procgroup"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const ConfigFileName = "ocel.config.ts"

const scratchDirName = ".ocel"

const initHint = "run `ocel init` to create one"

var defaultDiscoveryPaths = []string{"ocel"}

type Discovery struct {
	Paths []string
}

type ProviderDescriptor struct {
	Package string
	Options json.RawMessage
}

type EdgeDescriptor struct {
	Kind string
}

type DNSDescriptor struct {
	Kind string
	Zone string
}

type Build struct {
	Dockerfile string
	Context    string
	Command    string
}

type Registry struct {
	Server    string
	Namespace string
	Username  string
	Password  string
}

type Health struct {
	Path string
}

const (
	ArchX8664 = "x86_64"
	ArchARM64 = "arm64"
)

type Runtime struct {
	Name string
	Arch string
}

type App struct {
	Name       string
	Path       string
	Runtime    Runtime
	Entrypoint string
	Domains    map[string][]string
	Compute    string
	Build      *Build
	Health     *Health
	Folder     string
}

type Config struct {
	Slug          string
	Discovery     Discovery
	Provider      *ProviderDescriptor
	Edge          *EdgeDescriptor
	DNS           *DNSDescriptor
	AllowDegraded []string
	Apps          []App
	Links         []string
	Domains       map[string][]string
	Registry      *Registry
	Dir           string
	Path          string
}

func (c *Config) EdgeKind() edge.Kind {
	if c.Edge == nil {
		return ""
	}
	return edge.Kind(c.Edge.Kind)
}

func (c *Config) RequireProvider() (*ProviderDescriptor, error) {
	if c.Provider == nil {
		return nil, fmt.Errorf("no provider configured in %s — add `provider: awsProvider({...})` (from @ocel/provider-aws) to your config", filepath.Base(c.Path))
	}
	return c.Provider, nil
}

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
		Runtime    rawRuntime `json:"runtime"`
		Compute    string     `json:"compute"`
		Entrypoint string     `json:"entrypoint"`
		Folder     string     `json:"folder"`
		Domains    rawDomains `json:"domains"`
		Build      *struct {
			Dockerfile string `json:"dockerfile"`
			Context    string `json:"context"`
			Command    string `json:"command"`
		} `json:"build"`
		Health *struct {
			Path string `json:"path"`
		} `json:"health"`
	} `json:"apps"`
	Links         []string        `json:"links"`
	Domains       rawDomains      `json:"domains"`
	Edge          json.RawMessage `json:"edge"`
	DNS           json.RawMessage `json:"dns"`
	AllowDegraded []string        `json:"allowDegraded"`
	Registry      *struct {
		Server   string `json:"server"`
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"registry"`
}

type rawDomains struct {
	Production stringOrList `json:"production"`
	Preview    string       `json:"preview"`
}

type rawRuntime struct {
	Named bool
	Name  string
	Arch  string
}

func (r *rawRuntime) UnmarshalJSON(b []byte) error {
	if isAbsent(b) {
		*r = rawRuntime{}
		return nil
	}
	trimmed := bytes.TrimSpace(b)
	if trimmed[0] == '{' {
		var object struct {
			Name string `json:"name"`
			Arch string `json:"arch"`
		}
		if err := json.Unmarshal(b, &object); err != nil {
			return err
		}
		*r = rawRuntime{Named: true, Name: object.Name, Arch: object.Arch}
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	if one == "" {
		*r = rawRuntime{}
		return nil
	}
	*r = rawRuntime{Named: true, Name: one}
	return nil
}

type stringOrList []string

func (s *stringOrList) UnmarshalJSON(b []byte) error {
	if isAbsent(b) {
		*s = nil
		return nil
	}
	trimmed := bytes.TrimSpace(b)
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

func normalizeDomains(raw rawDomains) (map[string][]string, error) {
	domains := map[string][]string{}

	var preview string
	if raw.Preview != "" {
		preview = strings.ToLower(raw.Preview)
		if err := ValidatePreviewDomain(preview); err != nil {
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

func ValidatePreviewDomain(domain string) error {
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

func PreviewBaseDomain(previewDomain string) string {
	if !strings.HasPrefix(previewDomain, "*.") {
		return ""
	}
	return previewDomain[len("*."):]
}

func Resolve(ctx context.Context, startDir, explicitPath string) (*Config, error) {
	return resolve(ctx, startDir, explicitPath, false)
}

func ResolveOptional(ctx context.Context, startDir, explicitPath string) (*Config, error) {
	return resolve(ctx, startDir, explicitPath, true)
}

func resolve(ctx context.Context, startDir, explicitPath string, optional bool) (*Config, error) {
	if explicitPath != "" {
		configPath, err := explicitConfigFile(startDir, explicitPath)
		if err != nil {
			return nil, err
		}
		return load(ctx, configPath)
	}

	root, err := findProjectRoot(startDir)
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(root, ConfigFileName)
	if !isFile(configPath) {
		if optional {
			return &Config{
				Discovery: Discovery{Paths: defaultDiscoveryPaths},
				Dir:       root,
				Path:      configPath,
			}, nil
		}
		return nil, fmt.Errorf("no %s found in %s or any parent directory — %s", ConfigFileName, startDir, initHint)
	}
	return load(ctx, configPath)
}

func explicitConfigFile(startDir, explicitPath string) (string, error) {
	path := explicitPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(startDir, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if isDir(abs) {
		return "", fmt.Errorf("config file %s (from --config / OCEL_CONFIG) is a directory, not a config file", abs)
	}
	if !isFile(abs) {
		return "", fmt.Errorf("config file %s (from --config / OCEL_CONFIG) not found", abs)
	}
	return abs, nil
}

func load(ctx context.Context, configPath string) (*Config, error) {
	output, err := buildAndRun(ctx, configPath)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s failed to evaluate: %w", configPath, err)
	}

	var raw rawConfig
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("%s did not emit valid configuration: %w — %s", configPath, err, initHint)
	}
	if raw.Slug == "" {
		return nil, fmt.Errorf("%s is missing required \"slug\" — %s", configPath, initHint)
	}
	if err := ValidateSlug(raw.Slug); err != nil {
		return nil, fmt.Errorf("%s has an invalid \"slug\": %w", configPath, err)
	}

	var provider *ProviderDescriptor
	if raw.Provider != nil {
		options := raw.Provider.Options

		if len(options) == 0 || string(options) == "null" {
			options = json.RawMessage("{}")
		}

		provider = &ProviderDescriptor{Package: raw.Provider.Package, Options: options}
	}

	edge, err := normalizeEdge(raw.Edge)
	if err != nil {
		return nil, fmt.Errorf("%s has an invalid \"edge\": %w", configPath, err)
	}

	dns, err := normalizeDNS(raw.DNS, edge)
	if err != nil {
		return nil, fmt.Errorf("%s has an invalid \"dns\": %w", configPath, err)
	}

	allowDegraded, err := normalizeAllowDegraded(raw.AllowDegraded)
	if err != nil {
		return nil, fmt.Errorf("%s has an invalid \"allowDegraded\": %w", configPath, err)
	}

	apps, err := normalizeApps(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}
	paths := raw.Discovery.Paths
	if len(paths) == 0 {
		paths = slices.Clone(defaultDiscoveryPaths)
	}

	domains, err := normalizeDomains(raw.Domains)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}

	links, err := normalizeLinks(raw.Links)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}

	registry, err := normalizeRegistry(raw)
	if err != nil {
		return nil, fmt.Errorf("%s has an invalid \"registry\": %w", configPath, err)
	}

	return &Config{
		Slug:          raw.Slug,
		Discovery:     Discovery{Paths: paths},
		Provider:      provider,
		Edge:          edge,
		DNS:           dns,
		AllowDegraded: allowDegraded,
		Apps:          apps,
		Links:         links,
		Domains:       domains,
		Registry:      registry,
		Dir:           filepath.Dir(configPath),
		Path:          configPath,
	}, nil
}

var envVarName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func normalizeRegistry(raw rawConfig) (*Registry, error) {
	if raw.Registry == nil {
		return nil, nil
	}
	server, namespace, err := normalizeRegistryServer(strings.TrimSpace(raw.Registry.Server))
	if err != nil {
		return nil, err
	}
	password := strings.TrimSpace(raw.Registry.Password)
	if password == "" {
		return nil, errors.New("`password` names the environment variable holding the registry password or token, and a push authenticates, so there is no anonymous form to fall back to")
	}
	if !envVarName.MatchString(password) {
		return nil, errors.New("`password` is the name of an environment variable, not the secret itself, and this value is no variable name — write it as \"REGISTRY_TOKEN\", in upper case, and put the secret in the environment under that name")
	}
	return &Registry{
		Server:    server,
		Namespace: namespace,
		Username:  strings.TrimSpace(raw.Registry.Username),
		Password:  password,
	}, nil
}

func normalizeRegistryServer(server string) (string, string, error) {
	if server == "" {
		return "", "", errors.New("`server` is the only field naming where images land, so a registry without one would push wherever docker defaults to")
	}
	if strings.Contains(server, "://") {
		return "", "", errors.New("`server` is a registry host and the namespace under it, such as \"ghcr.io/acme\", not a URL: drop the scheme")
	}
	if strings.Contains(server, "@") {
		return "", "", errors.New("`server` carries credentials, and a registry password belongs in the environment `password` names, never in the config: write the host and namespace alone, such as \"ghcr.io/acme\"")
	}
	segments := strings.Split(server, "/")
	host := segments[0]
	if host == "" {
		return "", "", errors.New("`server` starts at a registry host, such as \"ghcr.io/acme\", and this one starts at a path separator")
	}
	for _, segment := range segments[1:] {
		if !naming.IsRepositorySegment(segment) {
			return "", "", errors.New("`server` names a host and the namespace an image sits under, such as \"ghcr.io/acme\", and a namespace segment is lowercase letters, digits and single separators")
		}
	}
	return host, strings.Join(segments[1:], "/"), nil
}

func normalizeLinks(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, name := range raw {
		link := strings.TrimSpace(name)
		if link == "" {
			return nil, fmt.Errorf("`links` holds an empty name — every entry names one resource your own infrastructure publishes")
		}
		if strings.Contains(link, naming.KeySeparator) {
			return nil, fmt.Errorf("link %q may not contain %q: it separates the fields of the key the published record is stored under", link, naming.KeySeparator)
		}
		if seen[link] {
			return nil, fmt.Errorf("`links` names %q twice — one entry binds it", link)
		}
		seen[link] = true
		out = append(out, link)
	}
	return out, nil
}

func knownNeeds() []string {
	return edge.NeedNames(edge.AllNeeds())
}

const edgeSpellings = "use `edge: cloudflare()` (from ocel/edge), name one of your provider's own edges, or omit it for the provider's default edge"

const dnsSpellings = "use `dns: cloudflareDns()` (from ocel/dns), `dns: route53()` (from @ocel/provider-aws/dns), or omit it"

const route53UnderCloudflare = "route53() cannot write the records a Cloudflare edge answers on — pair cloudflare() with cloudflareDns() (from ocel/dns), or drop the edge"

func isAbsent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null"
}

func normalizeEdge(raw json.RawMessage) (*EdgeDescriptor, error) {
	if isAbsent(raw) {
		return nil, nil
	}
	if string(bytes.TrimSpace(raw)) == "false" {
		return nil, errors.New("`edge: false` is gone — every deployment is fronted by some edge. Omit `edge` for the provider's default edge, or name one of your provider's own edges")
	}

	var marker struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &marker); err != nil || marker.Kind == "" {
		return nil, errors.New(edgeSpellings)
	}
	return &EdgeDescriptor{Kind: marker.Kind}, nil
}

func normalizeDNS(raw json.RawMessage, edge *EdgeDescriptor) (*DNSDescriptor, error) {
	if isAbsent(raw) {
		return nil, nil
	}

	var marker struct {
		Kind string `json:"kind"`
		Zone string `json:"zone"`
	}
	if err := json.Unmarshal(raw, &marker); err != nil || marker.Kind == "" {
		return nil, errors.New(dnsSpellings)
	}
	if marker.Kind == "route53" && edge != nil && edge.Kind == "cloudflare" {
		return nil, errors.New(route53UnderCloudflare)
	}
	return &DNSDescriptor{Kind: marker.Kind, Zone: marker.Zone}, nil
}

func normalizeAllowDegraded(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(raw))
	for _, name := range raw {
		if !edge.ValidNeed(edge.Need(name)) {
			return nil, fmt.Errorf("%q is not a need — the needs a deploy may degrade are %s", name, strings.Join(knownNeeds(), ", "))
		}
		out = append(out, name)
	}
	return out, nil
}

var dnsLabelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func ValidSlug(s string) bool {
	return ValidateSlug(s) == nil
}

func ValidateSlug(s string) error {
	if !dnsLabelPattern.MatchString(s) {
		return fmt.Errorf("%q must be a DNS label: lowercase letters, digits and hyphens, 1–63 characters, not starting or ending with a hyphen", s)
	}
	if strings.Contains(s, naming.FieldSeparator) {
		return fmt.Errorf("%q may not contain %q: it separates the fields of every name this project deploys, and separates the project from the preview in the hostname a preview is served on (\"<slug>%s<preview>[%s<app>].<domain>\") — use a single hyphen", s, naming.FieldSeparator, naming.FieldSeparator, naming.FieldSeparator)
	}
	return nil
}

func validAppName(name string) bool {
	return dnsLabelPattern.MatchString(name)
}

func normalizeApps(raw rawConfig) ([]App, error) {
	if len(raw.Apps) == 0 {
		return nil, nil
	}

	apps := make([]App, 0, len(raw.Apps))
	seen := make(map[string]bool, len(raw.Apps))
	boundFolders := make(map[string]string, len(raw.Apps))
	for _, a := range raw.Apps {
		if a.Name == "" {
			return nil, fmt.Errorf("app is missing required \"name\"")
		}
		if !validAppName(a.Name) {
			return nil, fmt.Errorf("invalid app name %q — an app name must be a DNS label: lowercase letters, digits and hyphens, 1–63 characters, not starting or ending with a hyphen. It is served as a label of a preview hostname (\"<preview>--%s.<your-preview-domain>\") and is a segment of every resource name this app deploys", a.Name, a.Name)
		}
		if seen[a.Name] {
			return nil, fmt.Errorf("duplicate app name %q — app names must be unique", a.Name)
		}
		seen[a.Name] = true

		if a.Path == "" {
			return nil, fmt.Errorf("app %q is missing required \"path\"", a.Name)
		}

		if a.Folder != "" {
			if err := envgate.ValidateFolder(a.Folder); err != nil {
				return nil, fmt.Errorf("app %q: %w", a.Name, err)
			}
			if other, taken := boundFolders[a.Folder]; taken {
				return nil, fmt.Errorf("apps %q and %q both bind folder %q — a folder holds one app's values, so two apps sharing one would defeat the divergence folders exist for", other, a.Name, a.Folder)
			}
			boundFolders[a.Folder] = a.Name
		}

		if a.Domains.Preview != "" {
			return nil, fmt.Errorf("app %q declares domains.preview %q: a preview domain binds to the whole project — every app is served from the project's one preview entrypoint — so declare it as a project-level domains.preview instead (per-app domains.production stays supported). Account-wide preview domains will be managed by the planned `ocel domains` command", a.Name, a.Domains.Preview)
		}

		domains, err := normalizeDomains(a.Domains)
		if err != nil {
			return nil, fmt.Errorf("app %q: %w", a.Name, err)
		}
		runtime, err := normalizeRuntime(a.Name, a.Runtime)
		if err != nil {
			return nil, err
		}
		var build *Build
		if a.Build != nil {
			dockerfile := strings.TrimSpace(a.Build.Dockerfile)
			if dockerfile == "" && a.Build.Dockerfile != "" {
				return nil, fmt.Errorf("app %q sets build.dockerfile to %q, which names no file: give it the path to a Dockerfile, or drop build.dockerfile to build %q from the Dockerfile beside it or from no configuration at all", a.Name, a.Build.Dockerfile, a.Name)
			}
			context := strings.TrimSpace(a.Build.Context)
			if context == "" && a.Build.Context != "" {
				return nil, fmt.Errorf("app %q sets build.context to %q, which names no directory: give it the directory the image is built from, or drop build.context to build %q from the workspace root its own directory sits in", a.Name, a.Build.Context, a.Name)
			}
			command := strings.TrimSpace(a.Build.Command)
			if command == "" && a.Build.Command != "" {
				return nil, fmt.Errorf("app %q sets build.command to %q, which names no command: give it the command that builds %q inside the image, or drop build.command to run the app's own build script", a.Name, a.Build.Command, a.Name)
			}
			build = &Build{Dockerfile: dockerfile, Context: context, Command: command}
		}
		var health *Health
		if a.Health != nil {
			path := strings.TrimSpace(a.Health.Path)
			if path != "" && !strings.HasPrefix(path, "/") {
				return nil, fmt.Errorf("app %q sets health.path to %q, which is not a path off the app's root: give it one starting with %q, or drop health.path to have %q probed at %q", a.Name, a.Health.Path, "/", a.Name, "/")
			}
			if path != "" && !providerkit.HealthCheckPath(path) {
				return nil, fmt.Errorf("app %q sets health.path to %q, and a probe asks one path of the process: give %q a path carrying no %q, %q, whitespace or control character, since a query or fragment names nothing the process is asked for", a.Name, a.Health.Path, a.Name, "?", "#")
			}
			health = &Health{Path: path}
		}
		apps = append(apps, App{
			Name:       a.Name,
			Path:       a.Path,
			Runtime:    runtime,
			Entrypoint: a.Entrypoint,
			Domains:    domains,
			Compute:    a.Compute,
			Build:      build,
			Health:     health,
			Folder:     a.Folder,
		})
	}

	return apps, nil
}

func normalizeRuntime(app string, raw rawRuntime) (Runtime, error) {
	if !raw.Named {
		return Runtime{}, nil
	}
	name := strings.TrimSpace(raw.Name)
	if name == "" {
		return Runtime{}, fmt.Errorf("app %q declares a runtime with no name: give it `runtime: %q` or `runtime: %q`, or drop runtime to have it read from the app's package.json", app, providerkit.RuntimeNode, providerkit.RuntimeNext)
	}
	if name != providerkit.RuntimeNode && name != providerkit.RuntimeNext {
		return Runtime{}, fmt.Errorf("app %q declares runtime %q, which nothing runs: the runtimes are %q and %q", app, name, providerkit.RuntimeNode, providerkit.RuntimeNext)
	}
	arch := strings.TrimSpace(raw.Arch)
	if arch != "" && arch != ArchX8664 && arch != ArchARM64 {
		return Runtime{}, fmt.Errorf("app %q declares runtime.arch %q, which names no architecture: the architectures are %q and %q", app, arch, ArchX8664, ArchARM64)
	}
	return Runtime{Name: name, Arch: arch}, nil
}

var reportedErrorKinds = []string{"BuildEnvError", "EnvDefinitionError"}

func recognizedErrorKinds() string {
	encoded, err := json.Marshal(reportedErrorKinds)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func buildAndRun(ctx context.Context, configPath string) ([]byte, error) {
	dir := filepath.Dir(configPath)
	outDir := filepath.Join(dir, scratchDirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", scratchDirName, err)
	}
	outfile := filepath.Join(outDir, bundleName(configPath))

	entry := fmt.Sprintf(`const kinds = %s;
try {
  const module = await import(%q);
  process.stdout.write(JSON.stringify(module.default));
} catch (error) {
  if (error instanceof Error && kinds.includes(error.name)) {
    console.error(error.name + ": " + error.message);
    process.exit(1);
  }
  throw error;
}
`, recognizedErrorKinds(), configPath)

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

	environment, err := configEnv(dir)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "node", outfile)
	cmd.Env = environment
	var stderr strings.Builder
	cmd.Stderr = &stderr
	procgroup.Guard(cmd)
	stdout, err := cmd.Output()
	if err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("node exited with error: %s", strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("run node: %w", err)
	}

	return stdout, nil
}

func configEnv(dir string) ([]string, error) {
	file, err := dotenv.Load(dir)
	if err != nil {
		return nil, err
	}

	environment := os.Environ()
	for key, value := range file.Values {
		if _, set := os.LookupEnv(key); set {
			continue
		}
		environment = append(environment, key+"="+value)
	}
	return environment, nil
}

func bundleName(configPath string) string {
	base := filepath.Base(configPath)
	if base == ConfigFileName {
		return "config.mjs"
	}
	return "config." + strings.TrimSuffix(base, filepath.Ext(base)) + ".mjs"
}

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
