package deploy

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/inlinebinding"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/cli/internal/valuestore"
	"github.com/ocelhq/ocel/cli/internal/variableeditor"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

type variablesRecovery struct {
	deps cmddeps.Deps
	cfg  *projectconfig.Config
	prov *providerclient.Provider
	tier environmentv1.Tier

	newDeclarations func(variables.EnvSource) *variables.Declarations

	command        string
	compute        string
	containerArchs map[string]string
	urls           map[string]string

	dry     bool
	enabled bool
}

func (r variablesRecovery) buildManifest(ctx context.Context, phase *run.Span, prebuilt bool) (*contractv1.Manifest, []inlinebinding.Record, error) {
	unit := phase.Unit(r.cfg.Slug, buildTitle(r.cfg, prebuilt))
	manifest, inline, err := r.build(ctx, phase, unit, prebuilt)
	unit.End(err)
	return manifest, inline, err
}

func buildTitle(cfg *projectconfig.Config, prebuilt bool) progress.Title {
	if !prebuilt || len(cfg.Apps) == 0 {
		return progress.Collecting.Title("the resources " + cfg.Slug + " declares")
	}
	return progress.Reading.Title("the prebuilt output of " + appList(cfg))
}

func nothingToDeploy(cfg *projectconfig.Config) string {
	if len(cfg.Apps) == 0 {
		return "Nothing to deploy: " + cfg.Slug + " declares no apps or resources"
	}
	return "Nothing to deploy: " + appList(cfg) + " built no function or image, and " + cfg.Slug + " declares no resources"
}

func appList(cfg *projectconfig.Config) string {
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
		return fmt.Sprintf("%d apps (%s)", len(names), runui.Listed(names))
	default:
		return fmt.Sprintf("%d apps (%s and %d more)", len(names), strings.Join(names[:3], ", "), len(names)-3)
	}
}

func (r variablesRecovery) build(ctx context.Context, phase, span *run.Span, prebuilt bool) (*contractv1.Manifest, []inlinebinding.Record, error) {
	declarations, err := r.declarations(ctx)
	var refusal *variables.MissingError
	if errors.As(err, &refusal) && r.enabled {
		if err := r.fill(ctx, span, declarations, refusal); err != nil {
			return nil, nil, err
		}
		declarations, err = r.declarations(ctx)
	}
	if err != nil {
		return nil, nil, err
	}
	manifest, inline, err := r.attempt(ctx, phase, span, declarations, prebuilt, 0)

	if !errors.As(err, &refusal) {
		return manifest, inline, err
	}
	if !r.enabled {
		r.createInEnvSource(ctx, span, declarations, refusal)
		return manifest, inline, err
	}
	if err := r.fill(ctx, span, declarations, refusal); err != nil {
		return nil, nil, err
	}
	if declarations, err = r.declarations(ctx); err != nil {
		return nil, nil, err
	}
	return r.attempt(ctx, phase, span, declarations, prebuilt, 1)
}

func (r variablesRecovery) declarations(ctx context.Context) (*variables.Declarations, error) {
	synced, err := valuestore.Store{Provider: r.prov, Config: r.cfg, Tier: r.tier}.SyncEnvSource(ctx)
	if problems := valuestore.CredentialProblems(err); len(problems) > 0 {
		declarations := r.newDeclarations(variablescope.ConfiguredEnvSource(r.cfg, r.tier))
		return declarations, declarations.RefuseCredentials(problems)
	}
	if err != nil {
		return nil, err
	}
	return r.newDeclarations(synced), nil
}

func (r variablesRecovery) createInEnvSource(ctx context.Context, span *run.Span, declarations *variables.Declarations, refusal *variables.MissingError) {
	source := declarations.Scope().EnvSource
	if !source.CanCreate || r.dry {
		return
	}
	vars, err := r.prov.Vars()
	if err != nil {
		return
	}
	for _, problem := range refusal.Problems {
		if problem.GetKind() != resourcesv1.VariableProblem_KIND_MISSING {
			continue
		}
		resp, err := vars.SetEnvSourceValue(ctx, &envvarsv1.SetEnvSourceValueRequest{
			Tier:        declarations.Scope().Tier,
			Coordinate:  &envvarsv1.Coordinate{Slug: r.cfg.Slug, Folder: problem.GetFolder(), Key: problem.GetKey()},
			Description: refusal.Description(problem.GetKey()),
		})
		switch {
		case err != nil:
			span.Warn(fmt.Sprintf("Could not create %s empty in %s, which lacks it: %v", problem.GetKey(), source.ID, err))
		case resp.GetAwaitingApproval():
			span.Warn(fmt.Sprintf("Asked %s to create %s empty: the change waits for approval there, then for you to fill it in", source.ID, problem.GetKey()))
		default:
			span.Warn(fmt.Sprintf("Created %s empty in %s, for you to fill in there", problem.GetKey(), source.ID))
		}
	}
}

func (r variablesRecovery) attempt(ctx context.Context, phase, unit *run.Span, declarations *variables.Declarations, prebuilt bool, retry int) (*contractv1.Manifest, []inlinebinding.Record, error) {
	attempt := unit.Trace(r.cfg.Slug, "build", progress.Attr{Key: progress.AttrKeyRetryCount, Value: strconv.Itoa(retry)})
	manifest, inline, err := collectAndBuildManifest(run.ContextWithSpan(ctx, attempt), r.deps, r.cfg, declarations, prebuilt, r.dry, phase, unit, r.compute, r.containerArchs, r.urls)
	attempt.End(err)
	return manifest, inline, err
}

func (r variablesRecovery) fill(ctx context.Context, span *run.Span, declarations *variables.Declarations, refusal *variables.MissingError) error {
	editor, err := r.deps.ServeVariableEditor(ctx, r.cfg, r.prov, r.tier, declarations, r.recovery(refusal))
	if err != nil {
		return err
	}
	defer editor.Close()

	resume := span.Hold(&streamv1.WaitingEvent{Missing: refusal.Variables(), Url: editor.URL})
	if err := r.deps.OpenBrowser(editor.URL); err != nil {
		span.Warn("Couldn't open your browser automatically — open the link above yourself.")
	}

	waiting := span.Trace(r.cfg.Slug, "await_human_input")
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

func (r variablesRecovery) recovery(refusal *variables.MissingError) *variableeditor.Recovery {
	missing := make([]variables.Cell, 0, len(refusal.Problems))
	for _, problem := range refusal.Problems {
		missing = append(missing, variables.Cell{Key: problem.GetKey(), Folder: problem.GetFolder()})
	}
	return &variableeditor.Recovery{Deploy: r.command, Missing: missing}
}

type abandonedRefusal struct {
	refusal *variables.MissingError
}

func (e *abandonedRefusal) Error() string {
	return e.refusal.Error() + "\n\n" + variableeditor.AbandonedMessage + "."
}

func (e *abandonedRefusal) Unwrap() []error {
	return []error{e.refusal, variableeditor.ErrAbandoned}
}
