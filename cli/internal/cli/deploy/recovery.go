package deploy

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type gateRecovery struct {
	deps    cmddeps.Deps
	cfg     *projectconfig.Config
	runner  *provider.Runner
	preview bool

	newGate func() *envgate.Gate

	command string
	compute string

	ui *runui.Session

	enabled bool
}

func (r gateRecovery) buildManifest(ctx context.Context, prebuilt bool) (*contractv1.Manifest, error) {
	gate := r.newGate()
	manifest, err := r.attempt(ctx, gate, prebuilt, 0)

	var refusal *envgate.Refusal
	if !r.enabled || !errors.As(err, &refusal) {
		return manifest, err
	}
	if err := r.fill(ctx, gate, refusal); err != nil {
		return nil, err
	}
	return r.attempt(ctx, r.newGate(), prebuilt, 1)
}

func (r gateRecovery) attempt(ctx context.Context, gate *envgate.Gate, prebuilt bool, retry int) (*contractv1.Manifest, error) {
	attemptCtx := ctx
	var span trace.Span
	if run := runtrace.FromContext(ctx); run != nil {
		attemptCtx, span = run.StartSpan(ctx, "build", runtrace.AttrRetryCount.Int(retry))
	}
	manifest, err := collectAndBuildManifest(attemptCtx, r.deps, r.cfg, gate, prebuilt, r.ui, r.compute)
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
