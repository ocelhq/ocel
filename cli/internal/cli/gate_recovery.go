package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/obs"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type gateRecovery struct {
	deps    deps
	cfg     *projectconfig.Config
	runner  *providerrunner.Runner
	preview bool

	newGate func() *envgate.Gate

	ui     *deployui.Session
	stdout io.Writer

	enabled bool
}

func (r gateRecovery) buildManifest(ctx context.Context, prebuilt bool) (*deploymentsv1.Manifest, error) {
	run := obs.FromContext(ctx)
	for attempt := 0; ; attempt++ {
		gate := r.newGate()

		attemptCtx := ctx
		var span trace.Span
		if run != nil {
			attemptCtx, span = run.StartSpan(ctx, "build", obs.AttrRetryCount.Int(attempt))
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
	session, err := r.deps.serveVarsUI(ctx, r.cfg, r.runner, r.preview, gate)
	if err != nil {
		return err
	}
	defer session.Close()

	r.ui.Waiting(refusal.Owed(), session.URL)
	if err := r.deps.openBrowser(session.URL); err != nil {
		fmt.Fprintln(r.stdout, "  Couldn't open your browser automatically — open the link above yourself.")
	}

	run := obs.FromContext(ctx)
	waitCtx := ctx
	var span trace.Span
	if run != nil {
		waitCtx, span = run.StartSpan(ctx, "await_human_input")
	}
	waitErr := session.Wait(waitCtx)
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
