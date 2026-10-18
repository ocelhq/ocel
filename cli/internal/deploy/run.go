package deploy

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variableeditor"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/progress"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type deployRun struct {
	dependencies Dependencies
	cfg          *project.Project
	provider     *providerprocess.Provider
	scope        variables.Scope
	command      string

	containerArchs map[string]string
	workerCeilings []provider.WorkerCeiling
	host           build.Host
	forwardsPorts  bool
	urls           map[string]string
	infra          *infraProvisioning
	preBuild       *project.LifecycleCommand

	prebuilt bool
	dry      bool
	canFill  bool

	declarations *variables.Declarations
	phase        *run.Span
	span         *run.Span
}

func newDeployRun(dependencies Dependencies, target Target, cfg *project.Project, process *providerprocess.Provider, env *environmentv1.Environment, facts preflightFacts, infra *infraProvisioning, opts Options, browser bool) deployRun {
	scope := variables.ScopeOf(cfg, env.GetTier(), env.GetIdentity())
	scope.Browser = browser
	return deployRun{
		dependencies:   dependencies,
		cfg:            cfg,
		provider:       process,
		scope:          scope,
		command:        target.command(),
		containerArchs: facts.containerArchs,
		workerCeilings: facts.workerCeilings,
		host:           build.ReadHost(process.Facts()),
		forwardsPorts:  process.Facts().GetForwardsPorts(),
		urls:           facts.urls,
		infra:          infra,
		preBuild:       findPreBuild(cfg, env),
		prebuilt:       opts.Prebuilt,
		dry:            opts.Dry,
		canFill:        !opts.Dry && browser,
	}
}

func (d deployRun) tier() environmentv1.Tier {
	return d.scope.Tier
}

func (d deployRun) buildManifest(ctx context.Context, phase *run.Span) (*contractv1.Manifest, []*bindingsv1.Binding, error) {
	d.phase = phase
	d.span = phase.Child(d.cfg.Slug, buildTitle(d.cfg, d.prebuilt))
	switch {
	case d.preBuild != nil && d.prebuilt:
		sayPrebuiltSkipsPreBuild(d.span, *d.preBuild, d.tier())
	case d.preBuild != nil:
		warnNpmPrebuildScripts(d.span, d.cfg)
	}
	held, release := d.infra.holdLease(ctx)
	defer release()
	manifest, inline, err := d.buildWithVariables(held)
	err = d.infra.lostLease(held, err)
	d.span.End(err)
	return manifest, inline, err
}

func buildTitle(cfg *project.Project, prebuilt bool) progress.Title {
	if !prebuilt || len(cfg.Apps) == 0 {
		return progress.Collecting.Title("the resources " + cfg.Slug + " declares")
	}
	return progress.Reading.Title("the prebuilt output of " + appList(cfg))
}

func nothingToDeploy(cfg *project.Project) string {
	if len(cfg.Apps) == 0 {
		return "Nothing to deploy: " + cfg.Slug + " declares no apps or resources"
	}
	return "Nothing to deploy: " + appList(cfg) + " built no function or image, and " + cfg.Slug + " declares no resources"
}

func appList(cfg *project.Project) string {
	names := make([]string, 0, len(cfg.Apps))
	for _, app := range cfg.Apps {
		names = append(names, app.Name)
	}
	switch {
	case len(names) == 0:
		return "the apps of " + cfg.Slug
	case len(names) == 1:
		return "app " + names[0]
	case len(names) <= 4:
		return fmt.Sprintf("%d apps (%s)", len(names), english.And(names))
	default:
		return fmt.Sprintf("%d apps (%s and %d more)", len(names), strings.Join(names[:3], ", "), len(names)-3)
	}
}

func (d deployRun) buildWithVariables(ctx context.Context) (*contractv1.Manifest, []*bindingsv1.Binding, error) {
	declarations, err := d.readDeclarations(ctx)
	var refusal *variables.MissingError
	if errors.As(err, &refusal) && d.canFill {
		if err := d.fill(ctx, declarations, refusal); err != nil {
			return nil, nil, err
		}
		declarations, err = d.readDeclarations(ctx)
	}
	if err != nil {
		return nil, nil, err
	}
	manifest, inline, err := d.attempt(ctx, declarations, 0)

	if !errors.As(err, &refusal) || !d.canFill {
		return manifest, inline, err
	}
	if err := d.fill(ctx, declarations, refusal); err != nil {
		return nil, nil, err
	}
	if declarations, err = d.readDeclarations(ctx); err != nil {
		return nil, nil, err
	}
	return d.attempt(ctx, declarations, 1)
}

func (d deployRun) newDeclarations(synced variables.EnvSource) *variables.Declarations {
	scope := d.scope
	scope.EnvSource = synced
	return variables.NewDeclarations(d.valueStore(), scope)
}

func (d deployRun) valueStore() variables.Store {
	return variables.Store{Provider: d.provider, Project: d.cfg, Tier: d.tier()}
}

func (d deployRun) readDeclarations(ctx context.Context) (*variables.Declarations, error) {
	synced, err := d.valueStore().SyncEnvSource(ctx)
	if problems := variables.CredentialProblems(err); len(problems) > 0 {
		declarations := d.newDeclarations(variables.ConfiguredEnvSource(d.cfg, d.tier()))
		return declarations, declarations.RefuseCredentials(problems)
	}
	if err != nil {
		return nil, err
	}
	return d.newDeclarations(synced), nil
}

func (d deployRun) attempt(ctx context.Context, declarations *variables.Declarations, retry int) (*contractv1.Manifest, []*bindingsv1.Binding, error) {
	attempt := d.span.Trace(d.cfg.Slug, "build", progress.Attr{Key: progress.AttrKeyRetryCount, Value: strconv.Itoa(retry)})
	d.declarations = declarations
	manifest, inline, err := d.collectBuildAndAssemble(run.ContextWithSpan(ctx, attempt))
	attempt.End(err)
	return manifest, inline, err
}

func (d deployRun) fill(ctx context.Context, declarations *variables.Declarations, refusal *variables.MissingError) error {
	editor, err := d.dependencies.ServeVariableEditor(ctx, d.cfg, d.provider, d.tier(), declarations, d.recovery(refusal))
	if err != nil {
		return err
	}
	defer editor.Close()

	resume := d.span.Hold(&streamv1.WaitingEvent{Missing: refusal.Variables(), Url: editor.URL})
	if err := d.dependencies.OpenBrowser(editor.URL); err != nil {
		d.span.Warn("Couldn't open your browser automatically — open the link above yourself.")
	}

	waiting := d.span.Trace(d.cfg.Slug, "await_human_input")
	waitErr := editor.Wait(ctx)
	waiting.End(waitErr)

	switch {
	case waitErr == nil:
		resume("the page was answered")
		return nil
	case errors.Is(waitErr, variableeditor.ErrAbandoned):
		return &abandonedRefusal{refusal: refusal}
	default:
		return waitErr
	}
}

func (d deployRun) recovery(refusal *variables.MissingError) *variableeditor.Recovery {
	missing := make([]variables.Cell, 0, len(refusal.Problems))
	for _, problem := range refusal.Problems {
		missing = append(missing, variables.Cell{Key: problem.GetKey(), Folder: problem.GetFolder()})
	}
	return &variableeditor.Recovery{Deploy: d.command, Missing: missing}
}

type abandonedRefusal struct {
	refusal *variables.MissingError
}

func (e *abandonedRefusal) Error() string {
	return e.refusal.Error() + "\n\n" + e.Detail()
}

func (e *abandonedRefusal) Variables() *streamv1.MissingVariables { return e.refusal.Variables() }

func (e *abandonedRefusal) Detail() string { return variableeditor.AbandonedMessage + "." }

func (e *abandonedRefusal) Unwrap() []error {
	return []error{e.refusal, variableeditor.ErrAbandoned}
}
