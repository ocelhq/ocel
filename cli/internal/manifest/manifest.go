package manifest

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Input struct {
	Project      *project.Project
	Tier         environmentv1.Tier
	Resources    []declaration.Resource
	Variables    map[string][]variables.Variable
	Built        build.Output
	Usages       []attribution.Usage
	DeploymentID func(projectDir, app string) (string, error)

	WorkerCeilings []provider.WorkerCeiling
}

func Assemble(in Input) (*contractv1.Manifest, error) {
	cfg := in.Project
	functions := servedByFunctions(in.Built.Functions, cfg)
	apps := appsOf(cfg.Dir, cfg.Apps, in.Usages, in.Built.Images)
	manifest, err := assemble(assembly{
		slug:         cfg.Slug,
		domains:      cfg.Domains,
		apps:         apps,
		declarations: declaredResources(cfg.Dir, in.Resources),
		bindings:     bindingsOf(cfg.BindingsFor(in.Tier)),
		functions:    functions,
		values:       in.Variables,
		ceilings:     in.WorkerCeilings,
	})
	if err != nil || in.DeploymentID == nil {
		return manifest, err
	}
	for _, app := range manifest.GetApps() {
		id, err := in.DeploymentID(cfg.Dir, app.GetName())
		if err != nil {
			return nil, err
		}
		app.DeploymentId = id
	}
	return manifest, nil
}

type assembly struct {
	slug         string
	domains      project.Domains
	apps         []app
	declarations []declaredResource
	bindings     []binding
	functions    []build.Function
	values       map[string][]variables.Variable
	ceilings     []provider.WorkerCeiling
}

func assemble(a assembly) (*contractv1.Manifest, error) {
	resolved, err := resolveTopicsAndWorkers(a.apps, a.declarations, a.ceilings)
	if err != nil {
		return nil, err
	}
	named := make(map[string]string, len(a.declarations)+len(a.functions))
	resources, seen, err := manifestResources(a.declarations, resolved.topics, named)
	if err != nil {
		return nil, err
	}
	if err := bindBindings(resources, a.bindings); err != nil {
		return nil, err
	}

	functionsByApp, err := manifestFunctions(a.functions, named)
	if err != nil {
		return nil, err
	}

	usages, err := manifestUsages(a.apps, seen)
	if err != nil {
		return nil, err
	}

	manifestApps, err := manifestAppsOf(a.apps, a.functions, functionsByApp, a.values)
	if err != nil {
		return nil, err
	}

	return &contractv1.Manifest{
		Slug:      a.slug,
		Resources: resources,
		Domains:   tierDomains(a.domains),
		Apps:      manifestApps,
		Usages:    usages,
		Workers:   resolved.workers,
	}, nil
}

type CollisionError struct {
	LogicalName string
	First       string
	Second      string
}

func (e *CollisionError) Error() string {
	return fmt.Sprintf(
		"%s and %s both name %q: rename one so the two differ by more than punctuation",
		e.First, e.Second, e.LogicalName,
	)
}

func manifestFunctions(functions []build.Function, named map[string]string) (map[string][]*contractv1.ManifestFunction, error) {
	byApp := make(map[string][]*contractv1.ManifestFunction)
	for _, f := range functions {
		if f.App == "" || f.Route == "" {
			return nil, fmt.Errorf("function %q of app %q needs both an app and a route name", f.Route, f.App)
		}

		logical := functionLogicalName(f.App, f.Route)
		described := fmt.Sprintf("route %q of app %q", f.Route, f.App)
		if prior, ok := named[logical]; ok {
			return nil, &CollisionError{LogicalName: logical, First: prior, Second: described}
		}
		named[logical] = described

		byApp[f.App] = append(byApp[f.App], &contractv1.ManifestFunction{
			LogicalName:  logical,
			Framework:    frameworkProto(f.Framework.Name, f.Framework.Arch),
			EntryFile:    f.EntryFile,
			ArtifactPath: f.ArtifactPath,
			RouteId:      f.RouteID,
		})
	}
	for _, appFunctions := range byApp {
		slices.SortFunc(appFunctions, func(a, b *contractv1.ManifestFunction) int {
			return strings.Compare(a.GetLogicalName(), b.GetLogicalName())
		})
	}
	return byApp, nil
}

func functionLogicalName(app, route string) string {
	return naming.Join(naming.FieldSeparator, string(naming.KindFunction), app, route)
}

func frameworkProto(name, arch string) *contractv1.Framework {
	if name == "" {
		return nil
	}
	return &contractv1.Framework{Name: name, Arch: arch}
}
