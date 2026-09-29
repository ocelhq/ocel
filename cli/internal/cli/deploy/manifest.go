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
	"github.com/ocelhq/ocel/cli/internal/appurl"
	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/inlinebinding"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/manifestwire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/cli/internal/workspace"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/constants"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func collectAndBuildManifest(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, declarations *variables.Declarations, prebuilt, dry bool, phase, scope *events.Scope, compute string, containerArchs map[string]string, urls map[string]string) (*contractv1.Manifest, []inlinebinding.Record, error) {
	captured := &boundedCapture{}
	tee := io.MultiWriter(scope.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED), captured)
	resources, err := deps.CollectDeclarations(ctx, cfg, declarations, tee, tee)
	if err != nil {
		return nil, nil, captured.annotate(err)
	}
	warnings, err := variables.LintFolders(declarations.Definitions(), variablescope.Apps(cfg), cfg.Path)
	if err != nil {
		return nil, nil, err
	}
	for _, warning := range warnings {
		scope.Warn(warning)
	}
	for _, warning := range variables.ListUndeclared(declarations.Declared(), declarations.Scope().EnvSource) {
		scope.Warn(warning)
	}
	if err := declarations.RefuseIncomplete(); err != nil {
		return nil, nil, err
	}
	inline, err := inlineRecords(ctx, deps, cfg, declarations, resources, scope)
	if err != nil {
		return nil, nil, err
	}

	variables, err := resolveVariables(ctx, declarations, cfg)
	if err != nil {
		return nil, nil, err
	}
	appurl.Prepend(cfg, variables, urls)

	configName := filepath.Base(cfg.Path)
	if err := checkAppPaths(cfg, configName); err != nil {
		return nil, nil, err
	}
	specs := appSpecs(cfg, variables)
	clients := clientApps(specs)
	steps := newBuildSteps(phase)
	if prebuilt {
		if err := clientenv.CheckFresh(cfg.Dir, clients); err != nil {
			return nil, nil, err
		}
		scope.Say("Using the prebuilt output in " + constants.ProjectStateDirName + "/output instead of building")
		scope.End(nil)
	} else {
		if err := clientenv.Generate(cfg.Dir, clients); err != nil {
			return nil, nil, err
		}
		if !dry {
			if err := clientenv.MapEnvImports(cfg.Dir, clients); err != nil {
				return nil, nil, err
			}
		}
		scope.End(nil)
		if err := steps.run(cfg.Slug, "Building "+appList(cfg), func() error {
			if err := deps.BuildApp(ctx, cfg, buildEnv(specs), steps.output("Building app ")); err != nil {
				return err
			}
			return clientenv.Record(cfg.Dir, clients)
		}); err != nil {
			return nil, nil, err
		}
	}

	var images map[string]string
	if err := steps.run(cfg.Slug, "Building the container images of "+cfg.Slug, func() (err error) {
		images, err = deps.BuildAppImages(ctx, cfg, containerArchs, steps.output("Building the image of app "))
		return err
	}); err != nil {
		return nil, nil, err
	}

	var manifest *contractv1.Manifest
	if err := steps.run(cfg.Slug, "Assembling the deploy manifest of "+cfg.Slug, func() (err error) {
		manifest, err = assembleManifest(ctx, deps, cfg, declarations, phase, resources, variables, images, compute, configName)
		return err
	}); err != nil || manifest == nil {
		return nil, nil, err
	}
	return manifest, inline, nil
}

func assembleManifest(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, declarations *variables.Declarations, phase *events.Scope, resources []declare.Resource, appValues map[string][]manifestbuilder.Variable, images map[string]string, compute, configName string) (*contractv1.Manifest, error) {
	functions, err := deps.CollectAppFunctions(cfg.Dir)
	if err != nil {
		return nil, err
	}
	functions = servedByFunctions(functions, cfg)

	edgeWarnings, err := variables.LintEdgeSecrets(declarations.Definitions(), variablescope.Apps(cfg), edgeApps(cfg))
	if err != nil {
		return nil, err
	}
	for _, warning := range edgeWarnings {
		phase.Warn(warning)
	}

	if len(functions) == 0 && len(images) == 0 {
		if len(resources) == 0 {
			return nil, nil
		}
		phase.Say(fmt.Sprintf("No app has a function or image to deploy, so this deploys only the %s %s declares", countOf(len(resources), "resource"), cfg.Slug))
	}

	attributionApps, err := toAttributionApps(cfg, functions, compute, configName)
	if err != nil {
		return nil, err
	}
	usages, err := attribution.Compute(ctx, cfg.Dir, attributionApps, toAttributionDeclarations(resources))
	if err != nil {
		return nil, err
	}

	manifest, err := manifestbuilder.Build(cfg.Slug, cfg.Domains, toApps(cfg.Dir, cfg.Apps, usages, compute, images, functions), compute, manifestwire.Declarations(cfg.Dir, resources), manifestwire.Bindings(cfg.BindingsFor(declarations.Scope().Tier)), functions, variablesByApp(appValues, functions))
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

type buildSteps struct {
	phase  *events.Scope
	shared io.Writer

	mu     sync.Mutex
	loose  bytes.Buffer
	failed bool
}

func newBuildSteps(phase *events.Scope) *buildSteps {
	return &buildSteps{phase: phase, shared: phase.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED)}
}

func (b *buildSteps) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loose.Write(p)
}

func (b *buildSteps) takeLoose() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	taken := bytes.Clone(b.loose.Bytes())
	b.loose.Reset()
	return taken
}

func (b *buildSteps) output(title string) appbuilder.Output {
	return appbuilder.Output{
		Shared: b,
		Unit: func(app string) (io.Writer, func(error)) {
			_, _ = b.shared.Write(b.takeLoose())
			unit := b.phase.Unit(app, title+app)
			return unit.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED), func(err error) {
				if err != nil {
					b.mu.Lock()
					b.failed = true
					b.mu.Unlock()
				}
				unit.End(err)
			}
		},
	}
}

func (b *buildSteps) run(subject, title string, step func() error) error {
	reserved := b.phase.ReserveUnit(subject, title)
	b.mu.Lock()
	b.failed = false
	b.mu.Unlock()
	err := step()
	b.mu.Lock()
	reported := b.failed
	b.mu.Unlock()
	loose := b.takeLoose()
	if err == nil || reported {
		_, _ = b.shared.Write(loose)
		return err
	}
	unit := reserved.Open()
	_, _ = unit.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED).Write(loose)
	unit.End(err)
	return err
}

func inlineRecords(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, declarations *variables.Declarations, resources []declare.Resource, scope *events.Scope) ([]inlinebinding.Record, error) {
	values, err := declarations.ResolveBindingVariables(ctx)
	if err != nil {
		return nil, err
	}
	records, err := inlinebinding.Build(cfg.BindingsFor(declarations.Scope().Tier), values, filepath.Base(cfg.Path))
	if err != nil || len(records) == 0 {
		return records, err
	}
	declared := inlinebinding.Declared{Postgres: map[string]string{}, Buckets: map[string]*resourcesv1.BucketConfig{}}
	for _, resource := range resources {
		if resource.Postgres != nil {
			declared.Postgres[resource.Name] = resource.Postgres.GetVersion()
		}
		if resource.Bucket != nil {
			declared.Buckets[resource.Name] = resource.Bucket
		}
	}
	warnings, err := inlinebinding.Verify(ctx, records, declared, inlinebinding.Probes{Postgres: deps.ProbePostgres, Bucket: deps.ProbeBucket})
	for _, warning := range warnings {
		scope.Warn(warning)
	}
	return records, err
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

func resolveVariables(ctx context.Context, declarations *variables.Declarations, cfg *projectconfig.Config) (map[string][]manifestbuilder.Variable, error) {
	definitions := declarations.Definitions()
	variables := make(map[string][]manifestbuilder.Variable, len(cfg.Apps))
	for _, app := range variablescope.Apps(cfg) {
		resolved, err := declarations.Resolve(ctx, app.Name)
		if err != nil {
			return nil, err
		}
		variables[app.Name] = appVariables(definitions, resolved)
	}
	return variables, nil
}

func appVariables(definitions []*resourcesv1.VariableDefinition, resolved map[string]variables.ResolvedValue) []manifestbuilder.Variable {
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
			Description:      definition.GetDescription(),
		})
	}
	return variables
}

func variablesByApp(variables map[string][]manifestbuilder.Variable, functions []manifestbuilder.Function) map[string][]manifestbuilder.Variable {
	root, ok := variables[variablescope.RootApp]
	if !ok {
		return variables
	}
	byApp := make(map[string][]manifestbuilder.Variable, len(functions))
	for _, f := range functions {
		byApp[f.App] = root
	}
	return byApp
}

type appSpec struct {
	name         string
	dir          string
	clientBundle bool
	variables    []manifestbuilder.Variable
}

func appSpecs(cfg *projectconfig.Config, variables map[string][]manifestbuilder.Variable) []appSpec {
	if len(cfg.Apps) == 0 {
		return []appSpec{{dir: cfg.Dir, clientBundle: discovery.ClientBundle(appbuild.FrameworkNode, cfg.Dir), variables: variables[variablescope.RootApp]}}
	}
	specs := make([]appSpec, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		dir := filepath.Join(cfg.Dir, a.Path)
		specs = append(specs, appSpec{
			name:         a.Name,
			dir:          dir,
			clientBundle: discovery.ClientBundle(a.Framework.Name, dir),
			variables:    variables[a.Name],
		})
	}
	return specs
}

func buildEnv(specs []appSpec) map[string]map[string]string {
	byApp := make(map[string]map[string]string, len(specs))
	for _, spec := range specs {
		env := make(map[string]string)
		for _, v := range spec.variables {
			if v.Class != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
				continue
			}
			env[v.Key] = v.Value
		}
		byApp[spec.name] = env
	}
	return byApp
}

func clientApps(specs []appSpec) []clientenv.App {
	apps := make([]clientenv.App, 0, len(specs))
	for _, spec := range specs {
		apps = append(apps, clientenv.App{Name: spec.name, Dir: spec.dir, ClientBundle: spec.clientBundle, Variables: spec.variables})
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
	return []string{variablescope.RootApp}
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

func toApps(projectDir string, apps []projectconfig.App, usages []attribution.Usage, compute string, images map[string]string, functions []manifestbuilder.Function) []manifestbuilder.App {
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
			Framework:       manifestwire.Framework(a.Framework),
			ClientBundle:    discovery.ClientBundle(a.Framework.Name, filepath.Join(projectDir, a.Path)),
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
			framework := unnamedFramework(name, functions)
			out = append(out, manifestbuilder.App{
				Name:         name,
				Framework:    framework,
				ClientBundle: appbuild.FrameworkBundlesClient(framework.Name),
				Compute:      compute,
				Usages:       byApp[name],
			})
		}
	}
	return out
}

func unnamedFramework(app string, functions []manifestbuilder.Function) manifestbuilder.Framework {
	for _, f := range functions {
		if f.App == app && f.Framework.Name != "" {
			return f.Framework
		}
	}
	return manifestbuilder.Framework{Name: appbuild.FrameworkNode}
}

func healthPathOf(app projectconfig.App) string {
	if app.Health == nil {
		return ""
	}
	return app.Health.Path
}

func workspaceMembers(inAnImage bool, appDir string) []string {
	if !inAnImage {
		return nil
	}
	located, err := workspace.Locate(appDir)
	if err != nil {
		return nil
	}
	return located.Members()
}

func toAttributionApps(cfg *projectconfig.Config, functions []manifestbuilder.Function, compute, configName string) ([]attribution.App, error) {
	detected := detectedApps(functions)
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return nil, err
	}
	apps := cfg.Apps
	container := compute == string(provider.ComputeContainer)

	if len(apps) == 0 {
		if len(detected) > 1 {
			return nil, fmt.Errorf(
				"this project builds %d apps (%s) but names none of them, so ocel cannot tell which source belongs to which and refuses to hand every app every resource: give each one a name and a path under `apps` in %s",
				len(detected), strings.Join(detected, ", "), configName,
			)
		}
		out := make([]attribution.App, 0, len(detected))
		for _, name := range detected {
			out = append(out, attribution.App{
				Name:      name,
				Path:      ".",
				Language:  discovery.LanguageOfApp(cfg.Dir),
				Roots:     roots,
				Container: container,
				Members:   workspaceMembers(container, cfg.Dir),
			})
		}
		return out, nil
	}

	named := make(map[string]bool, len(apps))
	out := make([]attribution.App, 0, len(apps))
	for _, a := range apps {
		named[a.Name] = true
		inAnImage := cmp.Or(a.Compute, compute) == string(provider.ComputeContainer)
		appDir := filepath.Join(cfg.Dir, a.Path)
		out = append(out, attribution.App{
			Name:      a.Name,
			Path:      a.Path,
			Language:  discovery.LanguageOf(a.Framework.Name, appDir),
			Roots:     roots,
			Container: inAnImage,
			Members:   workspaceMembers(inAnImage, appDir),
		})
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

func checkAppPaths(cfg *projectconfig.Config, configName string) error {
	for _, a := range cfg.Apps {
		if info, err := os.Stat(filepath.Join(cfg.Dir, a.Path)); err != nil || !info.IsDir() {
			return fmt.Errorf(
				"app %q has path %q, which is not a directory of this project: ocel reads an app's source to tell which resources it may be handed, so a path that names nothing would deploy %q alive with no resource at all — point `apps` in %s at %q's source",
				a.Name, a.Path, a.Name, configName, a.Name,
			)
		}
	}
	return nil
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

func toAttributionDeclarations(resources []declare.Resource) []attribution.Declaration {
	decls := make([]attribution.Declaration, len(resources))
	for i, r := range resources {
		decls[i] = attribution.Declaration{Type: r.Type, Name: r.Name, Source: r.Source}
	}
	return decls
}

func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
