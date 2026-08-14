package manifestbuilder

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

const SchemaVersion = "provider.v1"

type Declaration struct {
	Type     string
	ID       string
	Postgres *resourcesv1.PostgresConfig
	Bucket   *resourcesv1.BucketConfig
	Source   string
}

type App struct {
	Name      string
	Framework string
	Domains   map[string][]string
	Folder    string
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
	Name         string
	Runtime      string
	Handler      string
	ArtifactPath string
	Framework    string
	App          string
	RouteID      string
}

type DuplicateError struct {
	TypeToken    string
	ID           string
	FirstSource  string
	SecondSource string
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf(
		"manifestbuilder: duplicate resource declaration for type=%s id=%q: declared at %s and %s",
		e.TypeToken, e.ID, sourceOrUnknown(e.FirstSource), sourceOrUnknown(e.SecondSource),
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

func typeKind(t string) (naming.Kind, error) {
	kind, ok := naming.TokenKind(t)
	if !ok {
		return "", fmt.Errorf("manifestbuilder: unsupported resource type %q", t)
	}
	return kind, nil
}

func resourceLogicalName(kind naming.Kind, id string) string {
	return naming.Join(naming.FieldSeparator, string(kind), id)
}

func functionLogicalName(app, route string) string {
	return naming.Join(naming.FieldSeparator, string(naming.KindFunction), app, route)
}

func describeDeclaration(kind naming.Kind, d Declaration) string {
	return fmt.Sprintf("%s %q declared at %s", kind, d.ID, sourceOrUnknown(d.Source))
}

func describeFunction(f Function) string {
	return fmt.Sprintf("route %q of app %q", f.Name, f.App)
}

func Build(slug string, domains map[string][]string, apps []App, declarations []Declaration, functions []Function, variables map[string][]Variable) (*deploymentsv1.Manifest, error) {
	type identity struct {
		typ string
		id  string
	}
	seen := make(map[identity]Declaration, len(declarations))
	named := make(map[string]string, len(declarations)+len(functions))

	resources := make([]*deploymentsv1.ManifestResource, 0, len(declarations))
	for _, d := range declarations {
		if d.ID == "" {
			return nil, fmt.Errorf("manifestbuilder: declaration has empty resource id")
		}

		kind, err := typeKind(d.Type)
		if err != nil {
			return nil, err
		}

		id := identity{d.Type, d.ID}
		if prior, ok := seen[id]; ok {
			return nil, &DuplicateError{
				TypeToken:    string(kind),
				ID:           d.ID,
				FirstSource:  prior.Source,
				SecondSource: d.Source,
			}
		}
		seen[id] = d

		logical := resourceLogicalName(kind, d.ID)
		described := describeDeclaration(kind, d)
		if prior, ok := named[logical]; ok {
			return nil, &CollisionError{LogicalName: logical, First: prior, Second: described}
		}
		named[logical] = described

		resource := &deploymentsv1.ManifestResource{
			LogicalName: logical,
			Resource: &resourcesv1.ResourceIdentifier{
				Type: d.Type,
				Name: d.ID,
			},
		}
		if d.Postgres != nil {
			resource.Config = &deploymentsv1.ManifestResource_Postgres{Postgres: d.Postgres}
		}
		if d.Bucket != nil {
			resource.Config = &deploymentsv1.ManifestResource_Bucket{Bucket: d.Bucket}
		}
		resources = append(resources, resource)
	}

	slices.SortFunc(resources, func(a, b *deploymentsv1.ManifestResource) int {
		return strings.Compare(a.LogicalName, b.LogicalName)
	})

	manifestFunctions := make([]*deploymentsv1.ManifestFunction, 0, len(functions))
	for _, f := range functions {
		if f.App == "" || f.Name == "" {
			return nil, fmt.Errorf("manifestbuilder: function %q of app %q needs both an app and a route name", f.Name, f.App)
		}

		logical := functionLogicalName(f.App, f.Name)
		described := describeFunction(f)
		if prior, ok := named[logical]; ok {
			return nil, &CollisionError{LogicalName: logical, First: prior, Second: described}
		}
		named[logical] = described

		manifestFunctions = append(manifestFunctions, &deploymentsv1.ManifestFunction{
			LogicalName:  logical,
			Runtime:      f.Runtime,
			Handler:      f.Handler,
			ArtifactPath: f.ArtifactPath,
			Framework:    f.Framework,
			RouteId:      f.RouteID,
			App:          f.App,
		})
	}
	slices.SortFunc(manifestFunctions, func(a, b *deploymentsv1.ManifestFunction) int {
		return strings.Compare(a.LogicalName, b.LogicalName)
	})

	return &deploymentsv1.Manifest{
		SchemaVersion: SchemaVersion,
		Slug:          slug,
		Resources:     resources,
		Functions:     manifestFunctions,
		Domains:       domainLists(domains),
		Apps:          buildApps(apps, functions, variables),
	}, nil
}

func domainLists(domains map[string][]string) map[string]*deploymentsv1.DomainList {
	if len(domains) == 0 {
		return nil
	}
	out := make(map[string]*deploymentsv1.DomainList, len(domains))
	for class, hostnames := range domains {
		if len(hostnames) == 0 {
			continue
		}
		out[class] = &deploymentsv1.DomainList{Hostnames: hostnames}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func buildApps(apps []App, functions []Function, variables map[string][]Variable) []*deploymentsv1.ManifestApp {
	frameworkByApp := make(map[string]string, len(functions))
	for _, f := range functions {
		if f.App != "" && f.Framework != "" {
			if _, ok := frameworkByApp[f.App]; !ok {
				frameworkByApp[f.App] = f.Framework
			}
		}
	}

	manifestApps := make([]*deploymentsv1.ManifestApp, 0, len(apps))
	configured := make(map[string]bool, len(apps))
	for _, a := range apps {
		configured[a.Name] = true
		framework := a.Framework
		if framework == "" {
			framework = frameworkByApp[a.Name]
		}
		manifestApps = append(manifestApps, &deploymentsv1.ManifestApp{
			Name:      a.Name,
			Framework: framework,
			Domains:   domainLists(a.Domains),
			Variables: manifestVariables(variables[a.Name]),
			Folder:    a.Folder,
		})
	}

	for _, f := range functions {
		if f.App == "" || configured[f.App] {
			continue
		}
		configured[f.App] = true
		manifestApps = append(manifestApps, &deploymentsv1.ManifestApp{
			Name:      f.App,
			Framework: frameworkByApp[f.App],
			Variables: manifestVariables(variables[f.App]),
		})
	}

	slices.SortFunc(manifestApps, func(a, b *deploymentsv1.ManifestApp) int { return strings.Compare(a.Name, b.Name) })
	return manifestApps
}

func manifestVariables(variables []Variable) []*deploymentsv1.ManifestVariable {
	if len(variables) == 0 {
		return nil
	}
	out := make([]*deploymentsv1.ManifestVariable, 0, len(variables))
	for _, v := range variables {
		out = append(out, &deploymentsv1.ManifestVariable{Key: v.Key, Class: v.Class, Value: v.Value, Folder: v.Folder, Version: v.Version})
	}
	slices.SortFunc(out, func(a, b *deploymentsv1.ManifestVariable) int { return strings.Compare(a.Key, b.Key) })
	return out
}
