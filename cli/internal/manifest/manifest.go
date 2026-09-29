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
)

const ContractVersion = "provider.v1"

type Input struct {
	Project      *project.Project
	Tier         environmentv1.Tier
	Compute      string
	Resources    []declaration.Resource
	Variables    map[string][]variables.Variable
	Built        build.Output
	Usages       []attribution.Usage
	DeploymentID func(projectDir, app string) (string, error)
}

func Assemble(in Input) (*contractv1.Manifest, error) {
	cfg := in.Project
	functions := servedByFunctions(in.Built.Functions, cfg)
	apps := appsOf(cfg.Dir, cfg.Apps, in.Usages, in.Compute, in.Built.Images, functions)
	manifest, err := assemble(cfg.Slug, cfg.Domains, apps, in.Compute, declaredResources(cfg.Dir, in.Resources), bindingsOf(cfg.BindingsFor(in.Tier)), functions, variablesByApp(in.Variables, functions))
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

func assemble(slug string, domains project.Domains, apps []app, compute string, declarations []declaredResource, bindings []binding, functions []build.Function, values map[string][]variables.Variable) (*contractv1.Manifest, error) {
	if compute == "" {
		return nil, fmt.Errorf("project %q was built with no compute resolved — every app on the wire has to name the compute it runs on, and the manifest is built after preflight so that a provider's own answer is what fills it", slug)
	}

	named := make(map[string]string, len(declarations)+len(functions))
	resources, seen, err := manifestResources(declarations, named)
	if err != nil {
		return nil, err
	}
	if err := bindBindings(resources, bindings); err != nil {
		return nil, err
	}

	functionsByApp, err := manifestFunctions(functions, named)
	if err != nil {
		return nil, err
	}

	usages, err := manifestUsages(apps, seen)
	if err != nil {
		return nil, err
	}

	manifestApps, err := manifestAppsOf(apps, compute, functions, functionsByApp, values)
	if err != nil {
		return nil, err
	}

	return &contractv1.Manifest{
		SchemaVersion: ContractVersion,
		Slug:          slug,
		Resources:     resources,
		Domains:       tierDomains(domains),
		Apps:          manifestApps,
		Usages:        usages,
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
