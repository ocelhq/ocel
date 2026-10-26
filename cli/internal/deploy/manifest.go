package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/english"

	"github.com/ocelhq/ocel/cli/internal/appurl"
	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type collected struct {
	resources []declaration.Resource
	inline    []*bindingsv1.Binding
	values    map[string][]variables.Variable
	clients   []variables.ClientApp
	workers   build.HostedWorkers
	usages    []attribution.Usage
}

func (d *deployRun) collectBuildAndAssemble(ctx context.Context) (*contractv1.Manifest, []*bindingsv1.Binding, error) {
	c, err := d.collect(ctx)
	if err != nil {
		return nil, nil, err
	}
	steps := newBuildSteps(d.phase)
	var built build.Output
	if d.prebuilt {
		built, err = d.readPrebuilt(ctx, c)
	} else {
		built, err = d.provisionAndBuild(ctx, steps, c)
	}
	if err != nil {
		return nil, nil, err
	}
	var assembled *contractv1.Manifest
	if err := steps.run(d.cfg.Slug, progress.Assembling.Title("the deploy manifest of "+d.cfg.Slug), func() (err error) {
		assembled, err = d.assemble(c, built)
		return err
	}); err != nil || assembled == nil {
		return nil, nil, err
	}
	return assembled, c.inline, nil
}

func (d *deployRun) collect(ctx context.Context) (collected, error) {
	cfg, declarations, span := d.cfg, d.declarations, d.span
	captured := &boundedCapture{}
	tee := io.MultiWriter(span.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED), captured)
	resources, err := d.dependencies.CollectDeclarations(ctx, cfg, declarations, tee, tee)
	if err != nil {
		return collected{}, captured.annotate(err)
	}
	warnings, err := variables.LintFolders(declarations.Definitions(), variables.AppsOf(cfg), cfg.Path)
	if err != nil {
		return collected{}, err
	}
	for _, warning := range warnings {
		span.Warn(warning)
	}
	for _, warning := range variables.ListUndeclared(declarations.Declared(), declarations.Scope().EnvSource) {
		span.Warn(warning)
	}
	if err := declarations.RefuseIncomplete(); err != nil {
		return collected{}, err
	}
	inline, err := inlineBindings(ctx, cfg, declarations)
	if err != nil {
		return collected{}, err
	}

	values, err := declarations.ResolveVariables(ctx, variables.AppsOf(cfg))
	if err != nil {
		return collected{}, err
	}
	appurl.Prepend(cfg, values, d.urls)

	if err := checkAppPaths(cfg, filepath.Base(cfg.Path)); err != nil {
		return collected{}, err
	}
	placement, err := manifest.PlaceConsumers(cfg, resources)
	if err != nil {
		return collected{}, err
	}
	usages, err := manifest.FindUsages(ctx, cfg, resources)
	if err != nil {
		return collected{}, err
	}
	return collected{
		resources: resources,
		inline:    inline,
		values:    values,
		clients:   variables.ClientAppsOf(cfg, declarations.Definitions(), values),
		workers:   placement.HostedWorkers(),
		usages:    usages,
	}, nil
}

func (d *deployRun) readPrebuilt(ctx context.Context, c collected) (build.Output, error) {
	if err := variables.CheckClientValuesFresh(d.cfg.Dir, d.env, c.clients); err != nil {
		return build.Output{}, err
	}
	built, err := d.dependencies.ReadPrebuilt(ctx, d.cfg, d.containerArchs)
	if err != nil {
		return build.Output{}, err
	}
	d.span.Say("Using the prebuilt output in " + buildoutput.Dir + " instead of building")
	d.span.End(nil)
	return built, nil
}

func (d *deployRun) provisionAndBuild(ctx context.Context, steps *buildSteps, c collected) (build.Output, error) {
	for _, described := range d.cfg.DescribeFrameworks() {
		d.span.Say(described)
	}
	for _, warning := range findImageAppsBuiltWithoutValues(d.cfg, c.clients) {
		d.span.Warn(warning)
	}
	secrets, err := build.RevealSecrets(ctx, d.declarations, d.cfg, d.preBuild)
	if err != nil {
		return build.Output{}, err
	}
	d.span.End(nil)
	if err := d.provision(ctx, c); err != nil {
		return build.Output{}, err
	}
	return d.build(ctx, steps, c, secrets)
}

func (d *deployRun) provision(ctx context.Context, c collected) error {
	if d.dry {
		return nil
	}
	return d.infra.provision(ctx, c.resources, c.inline)
}

func (d *deployRun) build(ctx context.Context, steps *buildSteps, c collected, secrets map[string]map[string]string) (build.Output, error) {
	cfg := d.cfg
	runsPreBuild := d.preBuild != nil && !d.dry
	forwards, err := d.forwardPorts(ctx, steps, c, runsPreBuild)
	if err != nil {
		return build.Output{}, err
	}
	values := build.SplitVariablesByClass(c.clients, secrets)
	deliverForwards(forwards, values)
	if runsPreBuild {
		if err := d.runPreBuild(ctx, *d.preBuild, forwards, values, len(c.resources)); err != nil {
			return build.Output{}, errors.Join(err, forwards.Close())
		}
	}
	var built build.Output
	err = steps.run(cfg.Slug, progress.Building.Title(appList(cfg)), func() (err error) {
		built, err = d.dependencies.BuildApps(ctx, cfg, values, d.containerArchs, c.workers, d.host, steps.log())
		if err != nil {
			return err
		}
		return variables.RecordClientValues(cfg.Dir, d.env, c.clients)
	})
	return built, errors.Join(err, forwards.Close())
}

func (d *deployRun) assemble(c collected, built build.Output) (*contractv1.Manifest, error) {
	cfg := d.cfg
	onEdge, err := build.EdgeApps(cfg.Dir)
	if err != nil {
		return nil, err
	}
	edgeWarnings, err := variables.LintEdgeSecrets(d.declarations.Definitions(), variables.AppsOf(cfg), onEdge)
	if err != nil {
		return nil, err
	}
	for _, warning := range edgeWarnings {
		d.phase.Warn(warning)
	}

	if len(built.Functions) == 0 && len(built.Images) == 0 {
		if len(c.resources) == 0 {
			return nil, nil
		}
		only := fmt.Sprintf("No app has a function or image to deploy, so this deploys only the %s %s declares", english.Count(len(c.resources), "resource"), cfg.Slug)
		if cfg.HasUnclearRootApp() {
			d.phase.Warn(only + ", and " + cfg.RootAppUndetected.Error())
		} else {
			d.phase.Say(only)
		}
	}

	if err := manifest.RefuseUnnamedApps(cfg, built); err != nil {
		return nil, err
	}
	return manifest.Assemble(manifest.Input{
		Project:   cfg,
		Tier:      d.declarations.Scope().Tier,
		Resources: c.resources,
		Variables: c.values,
		Built:     built,
		Usages:    c.usages,
		BuildID:   d.dependencies.BuildID,

		WorkerCeilings: d.workerCeilings,
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

func (b *buildSteps) open(subject string, title progress.Title) *run.Span {
	_, _ = b.shared.Write(b.takeLoose())
	return b.phase.Child(subject, title)
}

func (b *buildSteps) log() build.Log {
	return build.Log{
		Shared: b,
		AppLog: func(app string) (io.Writer, func(error)) {
			child := b.open(app, progress.Building.Title("app "+app))
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

func findImageAppsBuiltWithoutValues(cfg *project.Project, clients []variables.ClientApp) []string {
	variablesOf := make(map[string][]variables.Variable, len(clients))
	for _, client := range clients {
		variablesOf[client.Name] = client.Variables
	}
	var warnings []string
	for _, app := range build.ImageApps(cfg.Apps) {
		var keys []string
		for _, v := range variablesOf[app.Name] {
			if v.Class == resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN && !processenv.IsInjected(app.Framework, v.Key) {
				keys = append(keys, v.Key)
			}
		}
		if len(keys) == 0 {
			continue
		}
		slices.Sort(keys)
		warnings = append(warnings, fmt.Sprintf("app %q builds as an image, and an image build gets none of its plaintext values yet: %s. Code that reads one while the app builds sees it unset", app.Name, strings.Join(slices.Compact(keys), ", ")))
	}
	return warnings
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
