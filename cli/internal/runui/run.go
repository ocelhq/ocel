package runui

import (
	"context"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
)

type Consent int

const (
	Convergent Consent = iota
	PlanFirst
)

type Spec struct {
	Command     string
	Consent     Consent
	Yes         bool
	Dry         bool
	Unattended  string
	Config      *projectconfig.Config
	Present     Presentation
	Trust       provider.Trust
	Interactive bool
	Stdout      io.Writer
}

type Body func(context.Context, *provider.Runner, *Session) error

func Run(ctx context.Context, spec Spec, body Body) error {
	if _, err := spec.Config.RequireProvider(); err != nil {
		return err
	}
	if err := spec.gate(); err != nil {
		return err
	}

	ctx, run, err := runtrace.Start(ctx, spec.Config.Dir, spec.Command)
	if err != nil {
		return err
	}
	defer run.Close()

	ui := New(spec.Stdout, run, spec.Present)
	defer ui.Close()

	provW := ui.BuildWriter()
	err = provider.Drive(ctx, spec.Config, provW, provW, trustFor(spec.Trust, ui), func(runner *provider.Runner) error {
		return body(ctx, runner, ui)
	})
	if err != nil {
		return fail(ctx, ui, err)
	}
	return nil
}

// TODO(#654): plan-first also owes a rendered plan and a consent prompt, and an
// all-KEEP plan skips consent — all three need the kit's plan contract first.
func (s Spec) gate() error {
	if s.Consent != PlanFirst || s.Dry || s.Yes || s.Interactive {
		return nil
	}
	return fmt.Errorf("`%s` needs a terminal to confirm what it will remove; to run it unattended, %s", s.Command, s.Unattended)
}

func trustFor(trust provider.Trust, ui *Session) provider.Trust {
	trust.Suspend = ui.Suspend
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
