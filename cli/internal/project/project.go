package project

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Provider struct {
	ID      string
	Options json.RawMessage
}

type Edge struct {
	Kind edge.Kind
}

type DNS struct {
	Kind string
	Zone string
}

type Project struct {
	Slug           string
	Transforms     []string
	DiscoveryPaths []string
	Provider       *Provider
	Edge           *Edge
	DNS            *DNS
	AllowDegraded  []edge.Need
	Apps           []App
	Bindings       []Binding
	Domains        Domains
	Registry       *Registry
	EnvSource      envsource.Tiers
	Dir            string
	Path           string
}

func (p *Project) EdgeKind() edge.Kind {
	if p.Edge == nil {
		return ""
	}
	return p.Edge.Kind
}

func (p *Project) EdgeSelection() *contractv1.EdgeSelection {
	selection := &contractv1.EdgeSelection{
		Kind:          string(p.EdgeKind()),
		AllowDegraded: edge.NeedNames(p.AllowDegraded),
	}
	if p.DNS != nil {
		selection.Dns = &contractv1.Dns{Kind: p.DNS.Kind, Zone: p.DNS.Zone}
	}
	return selection
}

func (p *Project) RequireProvider() (*Provider, error) {
	if p.Provider == nil {
		return nil, fmt.Errorf("no provider configured in %s — add `\"provider\": { \"<id>\": { … } }` keyed by the provider this project deploys through, one of %s", filepath.Base(p.Path), strings.Join(configdoc.ProviderIDs(), ", "))
	}
	return p.Provider, nil
}

func normalize(doc *configdoc.Document, configPath string) (*Project, error) {
	if doc.Slug == "" {
		return nil, fmt.Errorf("%s is missing required \"slug\" — %s", configPath, initHint)
	}
	if err := ValidateSlug(doc.Slug); err != nil {
		return nil, fmt.Errorf("%s has an invalid \"slug\": %w", configPath, err)
	}

	var provider *Provider
	if doc.Provider != nil {
		provider = &Provider{ID: doc.Provider.ID, Options: doc.Provider.Options}
	}

	var front *Edge
	if doc.Edge != nil {
		front = &Edge{Kind: edge.Kind(doc.Edge.ID)}
	}

	allowDegraded, err := normalizeAllowDegraded(doc.AllowDegraded)
	if err != nil {
		return nil, fmt.Errorf("%s has an invalid \"allowDegraded\": %w", configPath, err)
	}

	apps, err := normalizeApps(doc.Apps, filepath.Dir(configPath))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}
	if len(doc.Apps) == 0 {
		apps, err = rootApp(doc.Slug, filepath.Dir(configPath))
		if err != nil {
			return nil, err
		}
		if len(apps) > 0 && doc.Slug == naming.InfraApp {
			return nil, fmt.Errorf("%s names no apps, so the app at the project root is named after the slug, and %q is reserved for the stack holding each environment's shared infrastructure: list the app under \"apps\" with another name, or change the slug", configPath, naming.InfraApp)
		}
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

	var discoveryPaths []string
	if doc.Discovery != nil {
		discoveryPaths = doc.Discovery.Paths
	}

	return &Project{
		Slug:           doc.Slug,
		Transforms:     doc.Transforms,
		DiscoveryPaths: discoveryPaths,
		Provider:       provider,
		Edge:           front,
		DNS:            normalizeDNS(doc.DNS),
		AllowDegraded:  allowDegraded,
		Apps:           apps,
		Bindings:       bindings,
		Domains:        domains,
		Registry:       registry,
		EnvSource:      doc.EnvSource.Tiers(),
		Dir:            filepath.Dir(configPath),
		Path:           configPath,
	}, nil
}

func normalizeDNS(raw *configdoc.DnsDescriptor) *DNS {
	if raw == nil {
		return nil
	}
	return &DNS{Kind: raw.ID, Zone: raw.Options.Zone}
}

func normalizeAllowDegraded(raw []string) ([]edge.Need, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]edge.Need, 0, len(raw))
	for _, name := range raw {
		need := edge.Need(name)
		if !edge.ValidNeed(need) {
			return nil, fmt.Errorf("%q is not a need — the needs a deploy may degrade are %s", name, strings.Join(edge.NeedNames(edge.AllNeeds()), ", "))
		}
		out = append(out, need)
	}
	return out, nil
}
