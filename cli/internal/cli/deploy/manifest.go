package deploy

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/appimages"
	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/runui"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func collectAndBuildManifest(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, gate *envgate.Gate, prebuilt bool, ui *runui.Session, compute string) (*contractv1.Manifest, error) {
	buildOut := ui.BuildWriter()

	captured := &boundedCapture{}
	tee := io.MultiWriter(buildOut, captured)
	resources, err := deps.CollectDeclarations(ctx, cfg, gate, tee, tee)
	if err != nil {
		return nil, captured.annotate(err)
	}

	warnings, err := envgate.Lint(gate.Definitions(), envwire.Apps(cfg), cfg.Path)
	if err != nil {
		return nil, err
	}
	for _, warning := range warnings {
		ui.Warning(warning)
	}
	if err := gate.Check(); err != nil {
		return nil, err
	}

	variables, err := resolveVariables(ctx, gate, cfg)
	if err != nil {
		return nil, err
	}

	plans := appPlans(cfg, variables)
	clients := clientApps(plans)
	if prebuilt {
		if err := clientenv.CheckFresh(cfg.Dir, clients); err != nil {
			return nil, err
		}
		ui.Diagnostic("using prebuilt output in .ocel/output")
	} else {
		if err := clientenv.Generate(clients); err != nil {
			return nil, err
		}
		if err := deps.BuildApp(ctx, cfg, buildEnv(plans), buildOut); err != nil {
			return nil, err
		}
		if err := clientenv.Record(cfg.Dir, clients); err != nil {
			return nil, err
		}
	}

	images, err := deps.BuildAppImages(ctx, cfg, buildOut)
	if err != nil {
		return nil, err
	}

	functions, err := deps.CollectAppFunctions(cfg.Dir)
	if err != nil {
		return nil, err
	}
	functions = servedByFunctions(functions, cfg)

	edgeWarnings, err := envgate.LintEdge(gate.Definitions(), envwire.Apps(cfg), edgeApps(cfg))
	if err != nil {
		return nil, err
	}
	for _, warning := range edgeWarnings {
		ui.Warning(warning)
	}

	if len(functions) == 0 && len(images) == 0 {
		if len(resources) == 0 {
			return nil, nil
		}
		ui.Diagnostic("no functions to deploy; deploying infrastructure only")
	}

	attributionApps, err := toAttributionApps(cfg, functions, compute)
	if err != nil {
		return nil, err
	}
	usages, err := attribution.Compute(cfg.Dir, attributionApps, toAttributionDeclarations(resources))
	if err != nil {
		return nil, err
	}

	manifest, err := manifestbuilder.Build(cfg.Slug, cfg.Domains, toApps(cfg.Apps, usages, compute, images), compute, toDeclarations(cfg.Dir, resources), cfg.Links, functions, variablesByApp(variables, functions))
	if err != nil {
		return nil, err
	}
	for _, app := range manifest.GetApps() {
		id, err := deps.DeploymentID(cfg.Dir, app.GetName())
		if err != nil {
			return nil, err
		}
		app.DeploymentId = id
	}
	return manifest, nil
}

const maxCapturedDiscoveryOutput = 4096

type boundedCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *boundedCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := maxCapturedDiscoveryOutput - c.buf.Len(); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		c.buf.Write(p[:room])
	}
	return len(p), nil
}

func (c *boundedCapture) annotate(err error) error {
	c.mu.Lock()
	text := strings.TrimSpace(c.buf.String())
	c.mu.Unlock()
	if text == "" {
		return err
	}
	return fmt.Errorf("%w\n%s", err, text)
}

func resolveVariables(ctx context.Context, gate *envgate.Gate, cfg *projectconfig.Config) (map[string][]manifestbuilder.Variable, error) {
	definitions := gate.Definitions()
	variables := make(map[string][]manifestbuilder.Variable, len(cfg.Apps))
	for _, app := range envwire.Apps(cfg) {
		resolved, err := gate.Resolve(ctx, app.Name)
		if err != nil {
			return nil, err
		}
		variables[app.Name] = appVariables(definitions, resolved)
	}
	return variables, nil
}

func appVariables(definitions []*resourcesv1.VariableDefinition, resolved map[string]envgate.Resolved) []manifestbuilder.Variable {
	variables := make([]manifestbuilder.Variable, 0, len(definitions))
	for _, definition := range definitions {
		cell, ok := resolved[definition.GetKey()]
		if !ok {
			continue
		}
		variables = append(variables, manifestbuilder.Variable{
			Key:              definition.GetKey(),
			Class:            definition.GetClass(),
			Value:            cell.Value,
			Folder:           cell.Folder,
			Version:          cell.Version,
			ClientAccessible: definition.GetClientAccessible(),
			Source:           definition.GetSource(),
			SchemaSource:     definition.GetSchemaSource(),
			Schema:           definition.GetHasSchema(),
		})
	}
	return variables
}

func variablesByApp(variables map[string][]manifestbuilder.Variable, functions []manifestbuilder.Function) map[string][]manifestbuilder.Variable {
	root, ok := variables[envwire.RootApp]
	if !ok {
		return variables
	}
	byApp := make(map[string][]manifestbuilder.Variable, len(functions))
	for _, f := range functions {
		byApp[f.App] = root
	}
	return byApp
}

type appPlan struct {
	name      string
	dir       string
	variables []manifestbuilder.Variable
}

func appPlans(cfg *projectconfig.Config, variables map[string][]manifestbuilder.Variable) []appPlan {
	if len(cfg.Apps) == 0 {
		return []appPlan{{dir: cfg.Dir, variables: variables[envwire.RootApp]}}
	}
	plans := make([]appPlan, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		plans = append(plans, appPlan{
			name:      a.Name,
			dir:       filepath.Join(cfg.Dir, a.Path),
			variables: variables[a.Name],
		})
	}
	return plans
}

func buildEnv(plans []appPlan) map[string]map[string]string {
	byApp := make(map[string]map[string]string, len(plans))
	for _, plan := range plans {
		env := make(map[string]string)
		for _, v := range plan.variables {
			if v.Class != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
				continue
			}
			env[v.Key] = v.Value
		}
		byApp[plan.name] = env
	}
	return byApp
}

func clientApps(plans []appPlan) []clientenv.App {
	apps := make([]clientenv.App, 0, len(plans))
	for _, plan := range plans {
		apps = append(apps, clientenv.App{Name: plan.name, Dir: plan.dir, Variables: plan.variables})
	}
	return apps
}

func edgeApps(cfg *projectconfig.Config) []string {
	built := appbuilder.EdgeApps(cfg.Dir)
	if len(cfg.Apps) > 0 {
		return built
	}
	if len(built) == 0 {
		return nil
	}
	return []string{envwire.RootApp}
}

func servedByFunctions(functions []manifestbuilder.Function, cfg *projectconfig.Config) []manifestbuilder.Function {
	containers := make(map[string]bool, len(cfg.Apps))
	for _, app := range appimages.Apps(cfg) {
		containers[app.Name] = true
	}
	if len(containers) == 0 {
		return functions
	}
	return slices.DeleteFunc(slices.Clone(functions), func(f manifestbuilder.Function) bool {
		return containers[f.App]
	})
}

func toApps(apps []projectconfig.App, usages []attribution.Usage, compute string, images map[string]string) []manifestbuilder.App {
	byApp := make(map[string][]manifestbuilder.Usage, len(apps))
	for _, u := range usages {
		byApp[u.App] = append(byApp[u.App], manifestbuilder.Usage{Type: u.Type, Name: u.Name, Files: u.Files})
	}

	out := make([]manifestbuilder.App, 0, len(apps))
	named := make(map[string]bool, len(apps))
	for _, a := range apps {
		named[a.Name] = true
		out = append(out, manifestbuilder.App{
			Name:            a.Name,
			Framework:       a.Framework,
			Compute:         a.Compute,
			Domains:         a.Domains,
			Folder:          a.Folder,
			Usages:          byApp[a.Name],
			Image:           images[a.Name],
			HealthCheckPath: healthPathOf(a),
		})
	}
	for _, name := range slices.Sorted(maps.Keys(byApp)) {
		if !named[name] {
			out = append(out, manifestbuilder.App{Name: name, Compute: compute, Usages: byApp[name]})
		}
	}
	return out
}

func healthPathOf(app projectconfig.App) string {
	if app.Health == nil {
		return ""
	}
	return app.Health.Path
}

func toAttributionApps(cfg *projectconfig.Config, functions []manifestbuilder.Function, compute string) ([]attribution.App, error) {
	detected := detectedApps(functions)
	apps := cfg.Apps
	configName := filepath.Base(cfg.Path)
	container := compute == string(providerkit.ComputeContainer)

	if len(apps) == 0 {
		if len(detected) > 1 {
			return nil, fmt.Errorf(
				"this project builds %d apps (%s) but names none of them, so ocel cannot tell which source belongs to which and refuses to hand every app every resource: give each one a name and a path under `apps` in %s",
				len(detected), strings.Join(detected, ", "), configName,
			)
		}
		out := make([]attribution.App, 0, len(detected))
		for _, name := range detected {
			out = append(out, attribution.App{Name: name, Path: ".", Container: container})
		}
		return out, nil
	}

	named := make(map[string]bool, len(apps))
	out := make([]attribution.App, 0, len(apps))
	for _, a := range apps {
		if info, err := os.Stat(filepath.Join(cfg.Dir, a.Path)); err != nil || !info.IsDir() {
			return nil, fmt.Errorf(
				"app %q has path %q, which is not a directory of this project: ocel reads an app's source to tell which resources it may be handed, so a path that names nothing would deploy %q alive with no resource at all — point `apps` in %s at %q's source",
				a.Name, a.Path, a.Name, configName, a.Name,
			)
		}
		named[a.Name] = true
		out = append(out, attribution.App{Name: a.Name, Path: a.Path, Container: cmp.Or(a.Compute, compute) == string(providerkit.ComputeContainer)})
	}

	var unnamed []string
	for _, name := range detected {
		if !named[name] {
			unnamed = append(unnamed, name)
		}
	}
	if len(unnamed) > 0 {
		return nil, fmt.Errorf(
			"this project builds %s, which `apps` in %s does not name: ocel reads a named app's source to tell which resources it may be handed, and refuses to deploy an app it can attribute nothing to — give each one a name and a path under `apps`",
			strings.Join(unnamed, ", "), configName,
		)
	}
	return out, nil
}

func detectedApps(functions []manifestbuilder.Function) []string {
	var detected []string
	for _, f := range functions {
		if f.App != "" && !slices.Contains(detected, f.App) {
			detected = append(detected, f.App)
		}
	}
	slices.Sort(detected)
	return detected
}

func toDeclarations(configDir string, resources []declare.Resource) []manifestbuilder.Declaration {
	decls := make([]manifestbuilder.Declaration, len(resources))
	for i, r := range resources {
		var source string
		if frame, ok := attribution.DeclaringFrame(configDir, r.Stack); ok {
			source = frame.String()
		}
		decls[i] = manifestbuilder.Declaration{
			Type:     r.Type,
			Name:     r.Name,
			Postgres: r.Postgres,
			Bucket:   r.Bucket,
			Source:   source,
		}
	}
	return decls
}

func toAttributionDeclarations(resources []declare.Resource) []attribution.Declaration {
	decls := make([]attribution.Declaration, len(resources))
	for i, r := range resources {
		decls[i] = attribution.Declaration{Type: r.Type, Name: r.Name, Stack: r.Stack}
	}
	return decls
}
