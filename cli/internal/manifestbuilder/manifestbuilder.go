package manifestbuilder

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const ContractVersion = "provider.v1"

type Declaration struct {
	Type     linksv1.LinkType
	Name     string
	Postgres *resourcesv1.PostgresConfig
	Bucket   *resourcesv1.BucketConfig
	Source   string
}

type App struct {
	Name      string
	Framework string
	Compute   string
	Domains   map[string][]string
	Folder    string
	Usages    []Usage
}

type Usage struct {
	Type  linksv1.LinkType
	Name  string
	Files []string
}

type DanglingUsageError struct {
	App  string
	Type linksv1.LinkType
	Name string
}

func (e *DanglingUsageError) Error() string {
	return fmt.Sprintf(
		"manifestbuilder: app %q is attributed %s %q, which nothing in this project declares",
		e.App, e.Type, e.Name,
	)
}

type Variable struct {
	Key              string
	Class            resourcesv1.VariableClass
	Value            string
	Folder           string
	ClientAccessible bool
	Version          int64
}

type Function struct {
	Route        string
	Runtime      string
	Handler      string
	ArtifactPath string
	Framework    string
	App          string
	RouteID      string
}

type DuplicateError struct {
	TypeToken    string
	Name         string
	FirstSource  string
	SecondSource string
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf(
		"manifestbuilder: duplicate resource declaration for type=%s name=%q: declared at %s and %s",
		e.TypeToken, e.Name, sourceOrUnknown(e.FirstSource), sourceOrUnknown(e.SecondSource),
	)
}

type CollisionError struct {
	LogicalName string
	First       string
	Second      string
}

func (e *CollisionError) Error() string {
	return fmt.Sprintf(
		"manifestbuilder: %s and %s both name %q: rename one so the two differ by more than punctuation",
		e.First, e.Second, e.LogicalName,
	)
}

func sourceOrUnknown(source string) string {
	if source == "" {
		return "<unknown source>"
	}
	return source
}

func typeKind(t linksv1.LinkType) (naming.Kind, error) {
	kind, ok := naming.KindOf(t)
	if !ok {
		return "", fmt.Errorf("manifestbuilder: unsupported resource type %s", t)
	}
	return kind, nil
}

func resourceLogicalName(kind naming.Kind, name string) string {
	return naming.Join(naming.FieldSeparator, string(kind), name)
}

func functionLogicalName(app, route string) string {
	return naming.Join(naming.FieldSeparator, string(naming.KindFunction), app, route)
}

func describeDeclaration(kind naming.Kind, d Declaration) string {
	return fmt.Sprintf("%s %q declared at %s", kind, d.Name, sourceOrUnknown(d.Source))
}

func describeFunction(f Function) string {
	return fmt.Sprintf("route %q of app %q", f.Route, f.App)
}

type identity struct {
	typ  linksv1.LinkType
	name string
}

type UnboundLinkError struct {
	Link string
}

func (e *UnboundLinkError) Error() string {
	return fmt.Sprintf(
		"manifestbuilder: `links` binds %q, which nothing in this project declares — a link binds a resource your app already declares, so declare it or drop it from the list",
		e.Link,
	)
}

type AmbiguousLinkError struct {
	Link   string
	First  string
	Second string
}

func (e *AmbiguousLinkError) Error() string {
	return fmt.Sprintf(
		"manifestbuilder: `links` binds %q, which names both %s and %s — one published record cannot back two resources, so rename one of them",
		e.Link, e.First, e.Second,
	)
}

func Build(slug string, domains map[string][]string, apps []App, compute string, declarations []Declaration, links []string, functions []Function, variables map[string][]Variable) (*contractv1.Manifest, error) {
	if compute == "" {
		return nil, fmt.Errorf("manifestbuilder: project %q was built with no compute resolved — every app on the wire has to name the compute it runs on, and the manifest is built after preflight so that a provider's own answer is what fills it", slug)
	}

	seen := make(map[identity]Declaration, len(declarations))
	named := make(map[string]string, len(declarations)+len(functions))

	resources := make([]*contractv1.ManifestResource, 0, len(declarations))
	for _, d := range declarations {
		if d.Name == "" {
			return nil, fmt.Errorf("manifestbuilder: declaration has empty resource name")
		}

		kind, err := typeKind(d.Type)
		if err != nil {
			return nil, err
		}

		key := identity{d.Type, d.Name}
		if prior, ok := seen[key]; ok {
			return nil, &DuplicateError{
				TypeToken:    string(kind),
				Name:         d.Name,
				FirstSource:  prior.Source,
				SecondSource: d.Source,
			}
		}
		seen[key] = d

		logical := resourceLogicalName(kind, d.Name)
		described := describeDeclaration(kind, d)
		if prior, ok := named[logical]; ok {
			return nil, &CollisionError{LogicalName: logical, First: prior, Second: described}
		}
		named[logical] = described

		resource := &contractv1.ManifestResource{
			LogicalName: logical,
			Resource: &resourcesv1.ResourceIdentifier{
				Type: d.Type,
				Name: d.Name,
			},
		}
		if d.Postgres != nil {
			resource.Config = &contractv1.ManifestResource_Postgres{Postgres: d.Postgres}
		}
		if d.Bucket != nil {
			resource.Config = &contractv1.ManifestResource_Bucket{Bucket: d.Bucket}
		}
		resources = append(resources, resource)
	}

	slices.SortFunc(resources, func(a, b *contractv1.ManifestResource) int {
		return strings.Compare(a.LogicalName, b.LogicalName)
	})

	if err := bindLinks(resources, links); err != nil {
		return nil, err
	}

	manifestFunctions := make([]*contractv1.ManifestFunction, 0, len(functions))
	for _, f := range functions {
		if f.App == "" || f.Route == "" {
			return nil, fmt.Errorf("manifestbuilder: function %q of app %q needs both an app and a route name", f.Route, f.App)
		}

		logical := functionLogicalName(f.App, f.Route)
		described := describeFunction(f)
		if prior, ok := named[logical]; ok {
			return nil, &CollisionError{LogicalName: logical, First: prior, Second: described}
		}
		named[logical] = described

		manifestFunctions = append(manifestFunctions, &contractv1.ManifestFunction{
			LogicalName:  logical,
			Runtime:      f.Runtime,
			Handler:      f.Handler,
			ArtifactPath: f.ArtifactPath,
			Framework:    f.Framework,
			RouteId:      f.RouteID,
			App:          f.App,
		})
	}
	slices.SortFunc(manifestFunctions, func(a, b *contractv1.ManifestFunction) int {
		return strings.Compare(a.LogicalName, b.LogicalName)
	})

	usages, err := buildUsages(apps, seen)
	if err != nil {
		return nil, err
	}

	projectDomains, err := tierDomains(domains)
	if err != nil {
		return nil, err
	}

	manifestApps, err := buildApps(apps, compute, functions, variables)
	if err != nil {
		return nil, err
	}

	return &contractv1.Manifest{
		SchemaVersion: ContractVersion,
		Slug:          slug,
		Resources:     resources,
		Functions:     manifestFunctions,
		Domains:       projectDomains,
		Apps:          manifestApps,
		Usages:        usages,
	}, nil
}

func bindLinks(resources []*contractv1.ManifestResource, links []string) error {
	byID := make(map[string][]*contractv1.ManifestResource, len(resources))
	for _, r := range resources {
		id := r.GetResource().GetName()
		byID[id] = append(byID[id], r)
	}

	for _, link := range links {
		bound := byID[link]
		switch len(bound) {
		case 0:
			return &UnboundLinkError{Link: link}
		case 1:
			bound[0].Linked = true
		default:
			return &AmbiguousLinkError{Link: link, First: bound[0].GetLogicalName(), Second: bound[1].GetLogicalName()}
		}
	}
	return nil
}

func buildUsages(apps []App, declared map[identity]Declaration) ([]*contractv1.ManifestUsage, error) {
	merged := map[string]*contractv1.ManifestUsage{}
	for _, a := range apps {
		for _, u := range a.Usages {
			if _, ok := declared[identity{u.Type, u.Name}]; !ok {
				return nil, &DanglingUsageError{App: a.Name, Type: u.Type, Name: u.Name}
			}
			kind, err := typeKind(u.Type)
			if err != nil {
				return nil, err
			}

			logical := resourceLogicalName(kind, u.Name)
			edge, ok := merged[a.Name+naming.KeySeparator+logical]
			if !ok {
				edge = &contractv1.ManifestUsage{App: a.Name, Resource: logical}
				merged[a.Name+naming.KeySeparator+logical] = edge
			}
			for _, f := range u.Files {
				if !slices.Contains(edge.Files, f) {
					edge.Files = append(edge.Files, f)
				}
			}
		}
	}
	if len(merged) == 0 {
		return nil, nil
	}

	out := make([]*contractv1.ManifestUsage, 0, len(merged))
	for _, edge := range merged {
		slices.Sort(edge.Files)
		out = append(out, edge)
	}
	slices.SortFunc(out, func(a, b *contractv1.ManifestUsage) int {
		if c := strings.Compare(a.App, b.App); c != 0 {
			return c
		}
		return strings.Compare(a.Resource, b.Resource)
	})
	return out, nil
}

var domainClassTiers = map[string]environmentv1.Tier{
	"production": environmentv1.Tier_TIER_PRODUCTION,
	"preview":    environmentv1.Tier_TIER_PREVIEW,
}

func tierDomains(domains map[string][]string) ([]*contractv1.TierDomains, error) {
	out := make([]*contractv1.TierDomains, 0, len(domains))
	for class, hostnames := range domains {
		tier, ok := domainClassTiers[class]
		if !ok {
			return nil, fmt.Errorf("manifestbuilder: %q is not a domain class — `domains` accepts \"production\" and \"preview\"", class)
		}
		if len(hostnames) == 0 {
			continue
		}
		out = append(out, &contractv1.TierDomains{Tier: tier, Hostnames: hostnames})
	}
	if len(out) == 0 {
		return nil, nil
	}
	slices.SortFunc(out, func(a, b *contractv1.TierDomains) int { return cmp.Compare(a.GetTier(), b.GetTier()) })
	return out, nil
}

func buildApps(apps []App, compute string, functions []Function, variables map[string][]Variable) ([]*contractv1.ManifestApp, error) {
	frameworkByApp := make(map[string]string, len(functions))
	for _, f := range functions {
		if f.App != "" && f.Framework != "" {
			if _, ok := frameworkByApp[f.App]; !ok {
				frameworkByApp[f.App] = f.Framework
			}
		}
	}

	manifestApps := make([]*contractv1.ManifestApp, 0, len(apps))
	configured := make(map[string]bool, len(apps))
	for _, a := range apps {
		configured[a.Name] = true
		framework := a.Framework
		if framework == "" {
			framework = frameworkByApp[a.Name]
		}
		appCompute := a.Compute
		if appCompute == "" {
			appCompute = compute
		}
		appDomains, err := tierDomains(a.Domains)
		if err != nil {
			return nil, err
		}
		manifestApps = append(manifestApps, &contractv1.ManifestApp{
			Name:      a.Name,
			Framework: framework,
			Compute:   appCompute,
			Domains:   appDomains,
			Variables: manifestVariables(variables[a.Name]),
			Folder:    a.Folder,
		})
	}

	for _, f := range functions {
		if f.App == "" || configured[f.App] {
			continue
		}
		configured[f.App] = true
		manifestApps = append(manifestApps, &contractv1.ManifestApp{
			Name:      f.App,
			Framework: frameworkByApp[f.App],
			Compute:   compute,
			Variables: manifestVariables(variables[f.App]),
		})
	}

	slices.SortFunc(manifestApps, func(a, b *contractv1.ManifestApp) int { return strings.Compare(a.Name, b.Name) })
	return manifestApps, nil
}

func manifestVariables(variables []Variable) []*contractv1.ManifestVariable {
	if len(variables) == 0 {
		return nil
	}
	out := make([]*contractv1.ManifestVariable, 0, len(variables))
	for _, v := range variables {
		out = append(out, &contractv1.ManifestVariable{Key: v.Key, Class: v.Class, Value: v.Value, Folder: v.Folder, Version: v.Version})
	}
	slices.SortFunc(out, func(a, b *contractv1.ManifestVariable) int { return strings.Compare(a.Key, b.Key) })
	return out
}
