package runui

import (
	"context"
	"io"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type Spec struct {
	Command     string
	Consent     consent.Class
	Yes         bool
	Dry         bool
	Unattended  string
	Config      *projectconfig.Config
	Present     Presentation
	Trust       providerclient.Trust
	Interactive bool
	Stdout      io.Writer
	Stdin       io.Reader
}

type Body func(context.Context, *providerclient.Runner, *Session) error

type drive func(context.Context, *projectconfig.Config, io.Writer, io.Writer, providerclient.Trust, func(*providerclient.Runner) error) error

func Run(ctx context.Context, spec Spec, body Body) error {
	return run(ctx, spec, body, providerclient.Drive, providerclient.DriveDry)
}

func run(ctx context.Context, spec Spec, body Body, driveReal, driveDry drive) error {
	desc, err := spec.Config.RequireProvider()
	if err != nil {
		return err
	}
	g := spec.gate()
	if err := g.Refuse(); err != nil {
		return err
	}

	ctx, run, err := runtrace.Start(ctx, spec.Config.Dir, spec.Command)
	if err != nil {
		return err
	}
	defer run.Close()

	ui := New(spec.Stdout, run, spec.Present)
	ui.gate = g
	defer ui.Close()

	driveProvider := driveReal
	if spec.Dry {
		driveProvider = driveDry
	}
	stdout := ui.ProcessWriter(desc.ID, progressv1.Stream_STREAM_STDOUT)
	stderr := ui.ProcessWriter(desc.ID, progressv1.Stream_STREAM_STDERR)
	err = driveProvider(ctx, spec.Config, stdout, stderr, TrustFor(spec.Trust, sessionScope{ui}), func(runner *providerclient.Runner) error {
		return body(ctx, runner, ui)
	})
	if err != nil {
		return fail(ctx, ui, err)
	}
	return nil
}

func (s Spec) gate() consent.Gate {
	return consent.Gate{
		Command:     s.Command,
		Class:       s.Consent,
		Yes:         s.Yes,
		Dry:         s.Dry,
		Interactive: s.Interactive,
		Unattended:  s.Unattended,
		In:          s.Stdin,
		Out:         s.Stdout,
	}
}

func TrustFor(trust providerclient.Trust, s interface {
	Hold(*streamv1.WaitingEvent) func(reason string)
}) providerclient.Trust {
	trust.Hold = s.Hold
	return trust
}

func fail(ctx context.Context, ui *Session, err error) error {
	if ctx.Err() != nil {
		ui.Cancel()
		return &exitsig.ExitError{Code: exitsig.InterruptCode}
	}
	ui.Fail(err)
	return &exitsig.ExitError{Code: 1}
}
