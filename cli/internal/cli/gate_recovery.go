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

// gateRecovery turns a variable gate's refusal into a way out of it, in the run
// that was refused: it opens the bundled variables UI on the very gate that
// stopped the deploy, over the provider session the deploy already holds, and
// blocks until the developer says the matrix is done — then starts over.
//
// It sits after the preflight and the confirmation, which is what makes it
// worth having: those are already paid for, and re-running the command pays
// them again.
type gateRecovery struct {
	cfg      *projectconfig.Config
	provider *projectconfig.ProviderDescriptor
	runner   *providerrunner.Runner
	preview  bool

	// newGate builds the gate for one attempt. Each attempt needs its own: a
	// gate accumulates declarations, so running discovery twice against one
	// would double every definition it holds.
	newGate func() *envgate.Gate

	ui     *deployui.Session
	stdout io.Writer

	// enabled is the whole of the non-interactive rule. A run with no terminal
	// to block, or one that asked not to be handed a browser, keeps the hard
	// refusal it has always had.
	enabled bool
}

// buildManifest runs the pre-provision path, recovering from a gate refusal
// when it can and returning it when it cannot.
//
// Starting over means a new gate and a second discovery pass, not a re-read of
// the store. Whether a value satisfies its schema is only knowable inside the
// declaring process, and a write through the UI retracts discovery's complaint
// about the value it replaced — so a resume that only re-checked the old gate
// would accept a replacement that is invalid in a new way. The extra discovery
// run is the price of the word "validated".
//
// A matrix the developer marked done that still does not satisfy the gate
// reopens rather than ending the run: the premise of the whole path is that the
// developer does not re-run the command.
func (r gateRecovery) buildManifest(ctx context.Context, prebuilt bool, buildOut io.Writer) (*deploymentsv1.Manifest, error) {
	for {
		gate := r.newGate()
		manifest, err := collectAndBuildManifest(ctx, r.cfg, gate, prebuilt, buildOut)

		var refusal *envgate.Refusal
		if !r.enabled || !errors.As(err, &refusal) {
			return manifest, err
		}
		if err := r.fill(ctx, gate, refusal); err != nil {
			return nil, err
		}
	}
}

// fill opens the matrix on the refusing gate and blocks. It returns nil only
// for a matrix the developer finished; an abandoned session becomes the
// original refusal again, and an interrupted one becomes the context's error,
// which the caller renders as a cancellation.
func (r gateRecovery) fill(ctx context.Context, gate *envgate.Gate, refusal *envgate.Refusal) error {
	session, err := serveVarsUI(ctx, r.cfg, r.provider, r.runner, r.preview, gate)
	if err != nil {
		return err
	}
	defer session.Close()

	r.ui.Waiting(refusal.Owed(), session.URL)
	if err := openBrowser(session.URL); err != nil {
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

// abandonedRefusal is a recovery the developer walked away from: the page
// closed with the matrix still short. It reports the cells that are owed as
// well as the abandonment, because closing the page is not being told what is
// missing — and the refusal scrolled past a browser session ago.
type abandonedRefusal struct {
	refusal *envgate.Refusal
}

func (e *abandonedRefusal) Error() string {
	return e.refusal.Error() + "\n\n" + varsui.ErrAbandoned.Error() + "."
}

func (e *abandonedRefusal) Unwrap() []error {
	return []error{e.refusal, varsui.ErrAbandoned}
}
