package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type buildUI interface {
	BuildWriter() io.Writer
	Diagnostic(message string)
	Waiting(reason, url string)
	Resume()
	RestartBuild()
}

type gateRecovery struct {
	deps    cmddeps.Deps
	cfg     *projectconfig.Config
	runner  *provider.Runner
	preview bool

	newGate func() *envgate.Gate

	ui     buildUI
	stdout io.Writer

	enabled bool
}

func (r gateRecovery) buildManifest(ctx context.Context, prebuilt bool) (*contractv1.Manifest, error) {
	run := runtrace.FromContext(ctx)
	for attempt := 0; ; attempt++ {
		gate := r.newGate()

		attemptCtx := ctx
		var span trace.Span
		if run != nil {
			attemptCtx, span = run.StartSpan(ctx, "build", runtrace.AttrRetryCount.Int(attempt))
		}
		manifest, err := collectAndBuildManifest(attemptCtx, r.deps, r.cfg, gate, prebuilt, r.ui)
		endAttemptSpan(span, err)

		var refusal *envgate.Refusal
		if !r.enabled || !errors.As(err, &refusal) {
			return manifest, err
		}
		if err := r.fill(ctx, gate, refusal); err != nil {
			return nil, err
		}
	}
}

func (r gateRecovery) fill(ctx context.Context, gate *envgate.Gate, refusal *envgate.Refusal) error {
	varsSession, err := r.deps.ServeVarsUI(ctx, r.cfg, r.runner, r.preview, gate)
	if err != nil {
		return err
	}
	defer varsSession.Close()

	r.ui.Waiting(refusal.Owed(), varsSession.URL)
	if err := r.deps.OpenBrowser(varsSession.URL); err != nil {
		fmt.Fprintln(r.stdout, "  Couldn't open your browser automatically — open the link above yourself.")
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
		r.ui.RestartBuild()
		r.ui.Resume()
		return nil
	case errors.Is(waitErr, varsui.ErrAbandoned):
		return &abandonedRefusal{refusal: refusal}
	default:
		return waitErr
	}
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
