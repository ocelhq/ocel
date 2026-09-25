package deploy

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

type gateRecovery struct {
	deps    cmddeps.Deps
	cfg     *projectconfig.Config
	runner  *provider.Runner
	preview bool

	newGate func(envgate.Source) *envgate.Gate

	command        string
	compute        string
	containerArchs map[string]string
	urls           map[string]string

	ui *runui.Session

	enabled bool
}

func (r gateRecovery) buildManifest(ctx context.Context, prebuilt bool) (*contractv1.Manifest, error) {
	gate, err := r.gate(ctx)
	if err != nil {
		return nil, err
	}
	manifest, err := r.attempt(ctx, gate, prebuilt, 0)

	var refusal *envgate.Refusal
	if !errors.As(err, &refusal) {
		return manifest, err
	}
	if !r.enabled {
		r.offerToSource(ctx, gate, refusal)
		return manifest, err
	}
	if err := r.fill(ctx, gate, refusal); err != nil {
		return nil, err
	}
	if gate, err = r.gate(ctx); err != nil {
		return nil, err
	}
	return r.attempt(ctx, gate, prebuilt, 1)
}

func (r gateRecovery) gate(ctx context.Context) (*envgate.Gate, error) {
	synced, err := envwire.SyncEnvSource(ctx, r.runner, r.cfg, r.preview)
	if err != nil {
		return nil, err
	}
	return r.newGate(envwire.SourceOf(synced)), nil
}

func (r gateRecovery) offerToSource(ctx context.Context, gate *envgate.Gate, refusal *envgate.Refusal) {
	source := gate.Source()
	if !source.Owns() || !source.Writable {
		return
	}
	vars, err := r.runner.Vars()
	if err != nil {
		return
	}
	for _, problem := range refusal.Problems {
		if problem.GetKind() != resourcesv1.VariableProblem_KIND_MISSING {
			continue
		}
		_, err := vars.PutEnvSourceValue(ctx, &envvarsv1.PutEnvSourceValueRequest{
			Tier:        envwire.Tier(r.preview),
			Coordinate:  &envvarsv1.Coordinate{Slug: r.cfg.Slug, Folder: problem.GetFolder(), Key: problem.GetKey()},
			Description: refusal.Description(problem.GetKey()),
		})
		if err != nil {
			r.ui.Warning(fmt.Sprintf("%s holds no %s, and creating it there failed: %v", source.ID, problem.GetKey(), err))
			continue
		}
		r.ui.Warning(fmt.Sprintf("created %s empty in %s, for you to fill in there", problem.GetKey(), source.ID))
	}
}

func (r gateRecovery) attempt(ctx context.Context, gate *envgate.Gate, prebuilt bool, retry int) (*contractv1.Manifest, error) {
	attemptCtx := ctx
	var span trace.Span
	if run := runtrace.FromContext(ctx); run != nil {
		attemptCtx, span = run.StartSpan(ctx, "build", runtrace.AttrRetryCount.Int(retry))
	}
	manifest, err := collectAndBuildManifest(attemptCtx, r.deps, r.cfg, gate, prebuilt, r.ui, r.compute, r.containerArchs, r.urls)
	endAttemptSpan(span, err)
	return manifest, err
}

func (r gateRecovery) fill(ctx context.Context, gate *envgate.Gate, refusal *envgate.Refusal) error {
	varsSession, err := r.deps.ServeVarsUI(ctx, r.cfg, r.runner, r.preview, gate, r.recovery(refusal))
	if err != nil {
		return err
	}
	defer varsSession.Close()

	r.ui.Waiting(refusal.Owed(), varsSession.URL)
	if err := r.deps.OpenBrowser(varsSession.URL); err != nil {
		r.ui.Warning("Couldn't open your browser automatically — open the link above yourself.")
	}

	run := runtrace.FromContext(ctx)
	waitCtx := ctx
	var span trace.Span
	if run != nil {
		waitCtx, span = run.StartSpan(ctx, "await_human_input")
	}
	waitErr := varsSession.Wait(waitCtx)
	endAttemptSpan(span, waitErr)

	switch {
	case waitErr == nil:
		r.ui.Resume()
		return nil
	case errors.Is(waitErr, varsui.ErrAbandoned):
		return &abandonedRefusal{refusal: refusal}
	default:
		return waitErr
	}
}

func (r gateRecovery) recovery(refusal *envgate.Refusal) *varsui.Recovery {
	owed := make([]envgate.Cell, 0, len(refusal.Problems))
	for _, problem := range refusal.Problems {
		owed = append(owed, envgate.Cell{Key: problem.GetKey(), Folder: problem.GetFolder()})
	}
	return &varsui.Recovery{Deploy: r.command, Owed: owed}
}

func endAttemptSpan(span trace.Span, err error) {
	if span == nil {
		return
	}
	if err != nil {
		span.SetStatus(codes.Error, "")
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

type abandonedRefusal struct {
	refusal *envgate.Refusal
}

func (e *abandonedRefusal) Error() string {
	return e.refusal.Error() + "\n\n" + varsui.AbandonedMessage + "."
}

func (e *abandonedRefusal) Unwrap() []error {
	return []error{e.refusal, varsui.ErrAbandoned}
}
