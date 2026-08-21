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
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/procgroup"
	"github.com/ocelhq/ocel/pkg/naming"
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

type App struct {
	Name       string
	Path       string
	Framework  string
	Entrypoint string
	Domains    map[string][]string
	Compute    string
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
		Framework  string     `json:"framework"`
		Entrypoint string     `json:"entrypoint"`
		Folder     string     `json:"folder"`
		Domains    rawDomains `json:"domains"`
	} `json:"apps"`
	Links         []string        `json:"links"`
	Domains       rawDomains      `json:"domains"`
	Edge          json.RawMessage `json:"edge"`
	DNS           json.RawMessage `json:"dns"`
	AllowDegraded []string        `json:"allowDegraded"`
}

type rawDomains struct {
	Production stringOrList `json:"production"`
	Preview    string       `json:"preview"`
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

const defaultCompute = "serverless"

func Resolve(ctx context.Context, startDir, explicitPath string) (*Config, error) {
	return resolve(ctx, startDir, explicitPath, false)
}

func ResolveOptional(ctx context.Context, startDir, explicitPath string) (*Config, error) {
	return resolve(ctx, startDir, explicitPath, true)
}

func resolve(ctx context.Context, startDir, explicitPath string, optional bool) (*Config, error) {
	cfg, err := resolveConfig(ctx, startDir, explicitPath, optional)
	if err != nil {
		return nil, err
	}
	return applyTestSlugOverride(cfg)
}

func resolveConfig(ctx context.Context, startDir, explicitPath string, optional bool) (*Config, error) {
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

func applyTestSlugOverride(cfg *Config) (*Config, error) {
	slug := os.Getenv("OCEL_TEST_PROJECT_SLUG")
	if slug == "" {
		return cfg, nil
	}
	if err := ValidateSlug(slug); err != nil {
		return nil, fmt.Errorf("OCEL_TEST_PROJECT_SLUG has an invalid slug: %w", err)
	}
	cfg.Slug = slug
	return cfg, nil
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
		return nil, fmt.Errorf("could not read %s: %w — %s", configPath, err, initHint)
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

	domains, err := normalizeDomains(raw.Domains)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}

	links, err := normalizeLinks(raw.Links)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
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
		Dir:           filepath.Dir(configPath),
		Path:          configPath,
	}, nil
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
		apps = append(apps, App{
			Name:       a.Name,
			Path:       a.Path,
			Framework:  a.Framework,
			Entrypoint: a.Entrypoint,
			Domains:    domains,
			Compute:    defaultCompute,
			Folder:     a.Folder,
		})
	}

	return apps, nil
}

func buildAndRun(ctx context.Context, configPath string) ([]byte, error) {
	dir := filepath.Dir(configPath)
	outDir := filepath.Join(dir, scratchDirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", scratchDirName, err)
	}
	outfile := filepath.Join(outDir, bundleName(configPath))

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

	cmd := exec.CommandContext(ctx, "node", outfile)
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
