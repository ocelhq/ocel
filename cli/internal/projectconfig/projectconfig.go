package projectconfig

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Binding struct {
	Type     resourcesv1.ResourceType
	Name     string
	External string
}

type Discovery struct {
	Paths []string
}

type ProviderDescriptor struct {
	Name    string
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

type Runtime struct {
	Name string
	Arch string
}

func (r Runtime) Architecture() string { return providerkit.Architecture(r.Arch) }

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
	Bindings      []Binding
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
		return nil, fmt.Errorf("no provider configured in %s — add `\"provider\": { \"name\": … }` naming the provider this project deploys through", filepath.Base(c.Path))
	}
	return c.Provider, nil
}

func normalize(doc *configdoc.Document, configPath string) (*Config, error) {
	if doc.Slug == "" {
		return nil, fmt.Errorf("%s is missing required \"slug\" — %s", configPath, initHint)
	}
	if err := ValidateSlug(doc.Slug); err != nil {
		return nil, fmt.Errorf("%s has an invalid \"slug\": %w", configPath, err)
	}

	var provider *ProviderDescriptor
	if doc.Provider != nil {
		if strings.TrimSpace(doc.Provider.Name) == "" {
			return nil, fmt.Errorf("%s configures a provider with no \"name\" — name the provider this project deploys into", configPath)
		}
		options := doc.Provider.Options
		if len(options) == 0 || string(options) == "null" {
			options = json.RawMessage("{}")
		}
		provider = &ProviderDescriptor{Name: doc.Provider.Name, Options: options}
	}

	edgeDescriptor, err := normalizeEdge(doc.Edge)
	if err != nil {
		return nil, fmt.Errorf("%s has an invalid \"edge\": %w", configPath, err)
	}

	dns, err := normalizeDNS(doc.DNS, edgeDescriptor)
	if err != nil {
		return nil, fmt.Errorf("%s has an invalid \"dns\": %w", configPath, err)
	}

	allowDegraded, err := normalizeAllowDegraded(doc.AllowDegraded)
	if err != nil {
		return nil, fmt.Errorf("%s has an invalid \"allowDegraded\": %w", configPath, err)
	}

	apps, err := normalizeApps(doc.Apps, filepath.Dir(configPath))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}

	domains, err := normalizeProjectDomains(doc.Domains)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}

	bindings, err := normalizeBindings(doc.Bindings)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}

	registry, err := normalizeRegistry(doc.Registry)
	if err != nil {
		return nil, fmt.Errorf("%s has an invalid \"registry\": %w", configPath, err)
	}

	discovery := Discovery{}
	if doc.Discovery != nil {
		discovery.Paths = doc.Discovery.Paths
	}

	return &Config{
		Slug:          doc.Slug,
		Discovery:     discovery,
		Provider:      provider,
		Edge:          edgeDescriptor,
		DNS:           dns,
		AllowDegraded: allowDegraded,
		Apps:          apps,
		Bindings:      bindings,
		Domains:       domains,
		Registry:      registry,
		Dir:           filepath.Dir(configPath),
		Path:          configPath,
	}, nil
}

func normalizeProjectDomains(raw *configdoc.ProjectDomainConfig) (map[string][]string, error) {
	if raw == nil {
		return map[string][]string{}, nil
	}
	return normalizeDomains(raw.Production, raw.Preview)
}

func normalizeDomains(production configdoc.StringList, rawPreview string) (map[string][]string, error) {
	domains := map[string][]string{}

	var preview string
	if rawPreview != "" {
		preview = strings.ToLower(rawPreview)
		if err := ValidatePreviewDomain(preview); err != nil {
			return nil, err
		}
		domains["preview"] = []string{preview}
	}

	hosts, err := normalizeProductionDomains(production, preview)
	if err != nil {
		return nil, err
	}
	if len(hosts) > 0 {
		domains["production"] = hosts
	}

	return domains, nil
}

func normalizeProductionDomains(raw configdoc.StringList, preview string) ([]string, error) {
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

var envVarName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func normalizeRegistry(raw *configdoc.RegistryConfig) (*Registry, error) {
	if raw == nil {
		return nil, nil
	}
	server, namespace, err := normalizeRegistryServer(strings.TrimSpace(raw.Server))
	if err != nil {
		return nil, err
	}
	password := strings.TrimSpace(raw.Password)
	if password == "" {
		return nil, errors.New("`password` names the environment variable holding the registry password or token, and a push authenticates, so there is no anonymous form to fall back to")
	}
	if !envVarName.MatchString(password) {
		return nil, errors.New("`password` is the name of an environment variable, not the secret itself, and this value is no variable name — write it as \"REGISTRY_TOKEN\", in upper case, and put the secret in the environment under that name")
	}
	return &Registry{
		Server:    server,
		Namespace: namespace,
		Username:  strings.TrimSpace(raw.Username),
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

func normalizeBindings(raw configdoc.Bindings) ([]Binding, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]Binding, 0, len(raw))
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		typ, bindable := naming.ResourceTypeNamed(key)
		if _, ok := naming.BindableAs(typ); !bindable || !ok {
			return nil, fmt.Errorf("`bindings` is keyed by %q, and nothing publishes a record of that type — the types that can be bound are %s",
				key, strings.Join(configdoc.BindableTypes(), ", "))
		}
		named := raw[key]
		for _, declared := range slices.Sorted(maps.Keys(named)) {
			if strings.TrimSpace(declared) == "" {
				return nil, fmt.Errorf("`bindings.%s` is keyed by an empty name — the key is the name an app declares the resource under, and the value is the name the record is published under", key)
			}
			external := strings.TrimSpace(named[declared])
			if external == "" {
				return nil, fmt.Errorf("`bindings.%s.%s` names no published record — a binding always spells out the name the record is published under, even when it matches", key, declared)
			}
			if strings.Contains(external, naming.KeySeparator) {
				return nil, fmt.Errorf("published name %q may not contain %q: it separates the fields of the key the record is stored under", external, naming.KeySeparator)
			}
			out = append(out, Binding{Type: typ, Name: declared, External: external})
		}
	}
	slices.SortFunc(out, func(a, b Binding) int {
		if a.Type != b.Type {
			return cmp.Compare(a.Type, b.Type)
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

func knownNeeds() []string {
	return edge.NeedNames(edge.AllNeeds())
}

const route53UnderCloudflare = "route53 cannot write the records a Cloudflare edge answers on — pair a cloudflare edge with cloudflare dns, or drop the edge"

func normalizeEdge(raw *configdoc.EdgeDescriptor) (*EdgeDescriptor, error) {
	if raw == nil {
		return nil, nil
	}
	if strings.TrimSpace(raw.Kind) == "" {
		return nil, errors.New("`edge` names the edge the project's hostnames are served from, such as `{ \"kind\": \"cloudflare\" }` — omit it for the provider's default edge")
	}
	return &EdgeDescriptor{Kind: raw.Kind}, nil
}

func normalizeDNS(raw *configdoc.DnsDescriptor, edgeDescriptor *EdgeDescriptor) (*DNSDescriptor, error) {
	if raw == nil {
		return nil, nil
	}
	if strings.TrimSpace(raw.Kind) == "" {
		return nil, errors.New("`dns` names where the project's records are written, such as `{ \"kind\": \"cloudflare\" }` or `{ \"kind\": \"route53\" }`")
	}
	if raw.Kind == "route53" && edgeDescriptor != nil && edgeDescriptor.Kind == "cloudflare" {
		return nil, errors.New(route53UnderCloudflare)
	}
	return &DNSDescriptor{Kind: raw.Kind, Zone: raw.Zone}, nil
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

func normalizeApps(raw []configdoc.AppConfig, dir string) ([]App, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	apps := make([]App, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	boundFolders := make(map[string]string, len(raw))
	for _, a := range raw {
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

		var production configdoc.StringList
		if a.Domains != nil {
			production = a.Domains.Production
		}
		domains, err := normalizeDomains(production, "")
		if err != nil {
			return nil, fmt.Errorf("app %q: %w", a.Name, err)
		}
		runtime, err := resolveRuntime(a.Name, filepath.Join(dir, filepath.FromSlash(a.Path)), a.Framework, a.Arch, a.Compute)
		if err != nil {
			return nil, err
		}
		build, err := normalizeBuild(a)
		if err != nil {
			return nil, err
		}
		health, err := normalizeHealth(a)
		if err != nil {
			return nil, err
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

func normalizeBuild(a configdoc.AppConfig) (*Build, error) {
	if a.Build == nil {
		return nil, nil
	}
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
	return &Build{Dockerfile: dockerfile, Context: context, Command: command}, nil
}

func normalizeHealth(a configdoc.AppConfig) (*Health, error) {
	if a.Health == nil {
		return nil, nil
	}
	path := strings.TrimSpace(a.Health.Path)
	if path != "" && !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("app %q sets health.path to %q, which is not a path off the app's root: give it one starting with %q, or drop health.path to have %q probed at %q", a.Name, a.Health.Path, "/", a.Name, "/")
	}
	if path != "" && !providerkit.HealthCheckPath(path) {
		return nil, fmt.Errorf("app %q sets health.path to %q, and a probe asks one path of the process: give %q a path carrying no %q, %q, whitespace or control character, since a query or fragment names nothing the process is asked for", a.Name, a.Health.Path, a.Name, "?", "#")
	}
	return &Health{Path: path}, nil
}

func resolveRuntime(app, dir, framework, arch, compute string) (Runtime, error) {
	name, err := frameworkOf(app, dir, strings.TrimSpace(framework), strings.TrimSpace(compute))
	if err != nil {
		return Runtime{}, err
	}
	architecture, err := architectureOf(app, strings.TrimSpace(arch))
	if err != nil {
		return Runtime{}, err
	}
	return Runtime{Name: name, Arch: architecture}, nil
}
