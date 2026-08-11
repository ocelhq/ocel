package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type gateRecovery struct {
	deps     deps
	cfg      *projectconfig.Config
	provider *projectconfig.ProviderDescriptor
	runner   *providerrunner.Runner
	preview  bool

	newGate func() *envgate.Gate

	ui     *deployui.Session
	stdout io.Writer

	enabled bool
}

func (r gateRecovery) buildManifest(ctx context.Context, prebuilt bool, buildOut io.Writer) (*deploymentsv1.Manifest, error) {
	for {
		gate := r.newGate()
		manifest, err := collectAndBuildManifest(ctx, r.deps, r.cfg, gate, prebuilt, buildOut)

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
	session, err := r.deps.serveVarsUI(ctx, r.cfg, r.provider, r.runner, r.preview, gate)
	if err != nil {
		return err
	}
	defer session.Close()

	r.ui.Waiting(refusal.Owed(), session.URL)
	if err := r.deps.openBrowser(session.URL); err != nil {
		fmt.Fprintln(r.stdout, "  Couldn't open your browser automatically — open the link above yourself.")
	}

	waitErr := session.Wait(ctx)
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

type abandonedRefusal struct {
	refusal *envgate.Refusal
}

func (e *abandonedRefusal) Error() string {
	return e.refusal.Error() + "\n\n" + varsui.AbandonedMessage + "."
}

func (e *abandonedRefusal) Unwrap() []error {
	return []error{e.refusal, varsui.ErrAbandoned}
}
