package deploy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/appurl"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/inlinebinding"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type assembly struct {
	cfg            *project.Project
	declarations   *variables.Declarations
	prebuilt       bool
	dry            bool
	phase          *run.Span
	span           *run.Span
	containerArchs map[string]string
	workerCeilings []provider.WorkerCeiling
	host           build.Host
	urls           map[string]string
	infra          *infraProvisioning
}

func collectBuildAndAssemble(ctx context.Context, dependencies Dependencies, a assembly) (*contractv1.Manifest, []*bindingsv1.Binding, error) {
	cfg, declarations, span := a.cfg, a.declarations, a.span
	captured := &boundedCapture{}
	tee := io.MultiWriter(span.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED), captured)
	resources, err := dependencies.CollectDeclarations(ctx, cfg, declarations, tee, tee)
	if err != nil {
		return nil, nil, captured.annotate(err)
	}
	warnings, err := variables.LintFolders(declarations.Definitions(), variablescope.Apps(cfg), cfg.Path)
	if err != nil {
		return nil, nil, err
	}
	for _, warning := range warnings {
		span.Warn(warning)
	}
	for _, warning := range variables.ListUndeclared(declarations.Declared(), declarations.Scope().EnvSource) {
		span.Warn(warning)
	}
	if err := declarations.RefuseIncomplete(); err != nil {
		return nil, nil, err
	}
	inline, err := inlineBindings(ctx, cfg, declarations)
	if err != nil {
		return nil, nil, err
	}

	values, err := resolveVariables(ctx, declarations, cfg)
	if err != nil {
		return nil, nil, err
	}
	appurl.Prepend(cfg, values, a.urls)

	if err := checkAppPaths(cfg, filepath.Base(cfg.Path)); err != nil {
		return nil, nil, err
	}
	placement, err := manifest.PlaceConsumers(cfg, resources)
	if err != nil {
		return nil, nil, err
	}
	steps := newBuildSteps(a.phase)
	built, err := buildApps(ctx, dependencies, a, steps, clientenv.AppsOf(cfg, values), placement.HostedWorkers(), resources, inline)
	if err != nil {
		return nil, nil, err
	}

	var assembled *contractv1.Manifest
	if err := steps.run(cfg.Slug, progress.Assembling.Title("the deploy manifest of "+cfg.Slug), func() (err error) {
		assembled, err = assembleManifest(ctx, dependencies, a, resources, values, built)
		return err
	}); err != nil || assembled == nil {
		return nil, nil, err
	}
	return assembled, inline, nil
}

func buildApps(ctx context.Context, dependencies Dependencies, a assembly, steps *buildSteps, clients []clientenv.App, workers build.HostedWorkers, resources []declaration.Resource, inline []*bindingsv1.Binding) (build.Output, error) {
	cfg, span := a.cfg, a.span
	conflicts, err := build.FindNextConfigConflicts(cfg, a.host)
	if err != nil {
		return build.Output{}, err
	}
	for _, conflict := range conflicts {
		span.Warn(conflict.Warning())
	}
	if a.prebuilt {
		if err := clientenv.CheckFresh(cfg.Dir, clients); err != nil {
			return build.Output{}, err
		}
		built, err := dependencies.ReadPrebuilt(ctx, cfg, a.containerArchs)
		if err != nil {
			return build.Output{}, err
		}
		span.Say("Using the prebuilt output in " + buildoutput.Dir + " instead of building")
		span.End(nil)
		return built, nil
	}
	if err := clientenv.Generate(cfg.Dir, clients); err != nil {
		return build.Output{}, err
	}
	if !a.dry {
		if err := clientenv.MapEnvImports(cfg.Dir, clients); err != nil {
			return build.Output{}, err
		}
	}
	span.End(nil)
	if err := a.infra.provision(ctx, resources, inline); err != nil {
		return build.Output{}, err
	}
	var built build.Output
	err = steps.run(cfg.Slug, progress.Building.Title(appList(cfg)), func() (err error) {
		built, err = dependencies.BuildApps(ctx, cfg, build.VariablesOf(clients), a.containerArchs, workers, a.host, steps.log())
		if err != nil {
			return err
		}
		return clientenv.Record(cfg.Dir, clients)
	})
	return built, err
}

func assembleManifest(ctx context.Context, dependencies Dependencies, a assembly, resources []declaration.Resource, values map[string][]variables.Variable, built build.Output) (*contractv1.Manifest, error) {
	cfg := a.cfg
	onEdge, err := build.EdgeApps(cfg.Dir)
	if err != nil {
		return nil, err
	}
	edgeWarnings, err := variables.LintEdgeSecrets(a.declarations.Definitions(), variablescope.Apps(cfg), onEdge)
	if err != nil {
		return nil, err
	}
	for _, warning := range edgeWarnings {
		a.phase.Warn(warning)
	}

	if len(built.Functions) == 0 && len(built.Images) == 0 {
		if len(resources) == 0 {
			return nil, nil
		}
		a.phase.Say(fmt.Sprintf("No app has a function or image to deploy, so this deploys only the %s %s declares", countOf(len(resources), "resource"), cfg.Slug))
	}

	usages, err := manifest.FindUsages(ctx, cfg, built, resources)
	if err != nil {
		return nil, err
	}
	return manifest.Assemble(manifest.Input{
		Project:      cfg,
		Tier:         a.declarations.Scope().Tier,
		Resources:    resources,
		Variables:    values,
		Built:        built,
		Usages:       usages,
		DeploymentID: dependencies.DeploymentID,

		WorkerCeilings: a.workerCeilings,
	})
}

type buildSteps struct {
	phase  *run.Span
	shared io.Writer

	mu     sync.Mutex
	loose  bytes.Buffer
	failed bool
}

func newBuildSteps(phase *run.Span) *buildSteps {
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

func (b *buildSteps) log() build.Log {
	return build.Log{
		Shared: b,
		AppLog: func(app string) (io.Writer, func(error)) {
			_, _ = b.shared.Write(b.takeLoose())
			child := b.phase.Child(app, progress.Building.Title("app "+app))
			return child.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED), func(err error) {
				if err != nil {
					b.mu.Lock()
					b.failed = true
					b.mu.Unlock()
				}
				child.End(err)
			}
		},
	}
}

func (b *buildSteps) run(subject string, title progress.Title, step func() error) error {
	reserved := b.phase.ReserveChild(subject, title)
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
	child := reserved.Open()
	_, _ = child.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED).Write(loose)
	child.End(err)
	return err
}

func inlineBindings(ctx context.Context, cfg *project.Project, declarations *variables.Declarations) ([]*bindingsv1.Binding, error) {
	values, err := declarations.ResolveBindingVariables(ctx)
	if err != nil {
		return nil, err
	}
	return inlinebinding.Build(cfg.BindingsFor(declarations.Scope().Tier), values, filepath.Base(cfg.Path))
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

func resolveVariables(ctx context.Context, declarations *variables.Declarations, cfg *project.Project) (map[string][]variables.Variable, error) {
	definitions := declarations.Definitions()
	values := make(map[string][]variables.Variable, len(cfg.Apps))
	for _, app := range variablescope.Apps(cfg) {
		resolved, err := declarations.Resolve(ctx, app.Name)
		if err != nil {
			return nil, err
		}
		values[app.Name] = appVariables(definitions, resolved)
	}
	return values, nil
}

func appVariables(definitions []*resourcesv1.VariableDefinition, resolved map[string]variables.ResolvedValue) []variables.Variable {
	values := make([]variables.Variable, 0, len(definitions))
	for _, definition := range definitions {
		cell, ok := resolved[definition.GetKey()]
		if !ok {
			continue
		}
		values = append(values, variables.Variable{
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
	return values
}

func checkAppPaths(cfg *project.Project, configName string) error {
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

func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
