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
	"github.com/ocelhq/ocel/cli/internal/inlinebinding"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
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
	runner  *providerclient.Runner
	preview bool

	newGate func(envgate.EnvSource) *envgate.Gate

	command        string
	compute        string
	containerArchs map[string]string
	urls           map[string]string

	ui *runui.Session

	enabled bool
}

func (r gateRecovery) buildManifest(ctx context.Context, prebuilt bool) (*contractv1.Manifest, []inlinebinding.Record, error) {
	gate, err := r.gate(ctx)
	var refusal *envgate.Refusal
	if errors.As(err, &refusal) && r.enabled {
		if err := r.fill(ctx, gate, refusal); err != nil {
			return nil, nil, err
		}
		gate, err = r.gate(ctx)
	}
	if err != nil {
		return nil, nil, err
	}
	manifest, inline, err := r.attempt(ctx, gate, prebuilt, 0)

	if !errors.As(err, &refusal) {
		return manifest, inline, err
	}
	if !r.enabled {
		r.createInEnvSource(ctx, gate, refusal)
		return manifest, inline, err
	}
	if err := r.fill(ctx, gate, refusal); err != nil {
		return nil, nil, err
	}
	if gate, err = r.gate(ctx); err != nil {
		return nil, nil, err
	}
	return r.attempt(ctx, gate, prebuilt, 1)
}

func (r gateRecovery) gate(ctx context.Context) (*envgate.Gate, error) {
	synced, err := envwire.SyncEnvSource(ctx, r.runner, r.cfg, r.preview)
	if problems := envwire.CredentialProblems(err); len(problems) > 0 {
		gate := r.newGate(envwire.ConfiguredEnvSource(r.cfg, r.preview))
		return gate, gate.RefuseCredentials(problems)
	}
	if err != nil {
		return nil, err
	}
	return r.newGate(envwire.EnvSourceOf(synced)), nil
}

func (r gateRecovery) createInEnvSource(ctx context.Context, gate *envgate.Gate, refusal *envgate.Refusal) {
	source := gate.Scope().EnvSource
	if !source.Writable || r.ui.Dry() {
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
		resp, err := vars.CreateEnvSourceValue(ctx, &envvarsv1.CreateEnvSourceValueRequest{
			Tier:        gate.Scope().Tier(),
			Coordinate:  &envvarsv1.Coordinate{Slug: r.cfg.Slug, Folder: problem.GetFolder(), Key: problem.GetKey()},
			Description: refusal.Description(problem.GetKey()),
		})
		switch {
		case err != nil:
			r.ui.Warning(fmt.Sprintf("%s has no %s, and creating it there failed: %v", source.ID, problem.GetKey(), err))
		case resp.GetAwaitingApproval():
			r.ui.Warning(fmt.Sprintf("asked %s to create %s empty; the change waits for approval there, then for you to fill it in", source.ID, problem.GetKey()))
		default:
			r.ui.Warning(fmt.Sprintf("created %s empty in %s, for you to fill in there", problem.GetKey(), source.ID))
		}
	}
}

func (r gateRecovery) attempt(ctx context.Context, gate *envgate.Gate, prebuilt bool, retry int) (*contractv1.Manifest, []inlinebinding.Record, error) {
	attemptCtx := ctx
	var span trace.Span
	if run := runtrace.FromContext(ctx); run != nil {
		attemptCtx, span = run.StartSpan(ctx, "build", runtrace.AttrRetryCount.Int(retry))
	}
	manifest, inline, err := collectAndBuildManifest(attemptCtx, r.deps, r.cfg, gate, prebuilt, r.ui, r.compute, r.containerArchs, r.urls)
	endAttemptSpan(span, err)
	return manifest, inline, err
}

func (r gateRecovery) fill(ctx context.Context, gate *envgate.Gate, refusal *envgate.Refusal) error {
	varsSession, err := r.deps.ServeVarsUI(ctx, r.cfg, r.runner, r.preview, gate, r.recovery(refusal))
	if err != nil {
		return err
	}
	defer varsSession.Close()

	r.ui.Waiting(refusal.Missing(), varsSession.URL)
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
	missing := make([]envgate.Cell, 0, len(refusal.Problems))
	for _, problem := range refusal.Problems {
		missing = append(missing, envgate.Cell{Key: problem.GetKey(), Folder: problem.GetFolder()})
	}
	return &varsui.Recovery{Deploy: r.command, Missing: missing}
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
