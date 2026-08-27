package runui

import (
	"context"
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
	Stdin       io.Reader
}

type Body func(context.Context, *provider.Runner, *Session) error

func Run(ctx context.Context, spec Spec, body Body) error {
	if _, err := spec.Config.RequireProvider(); err != nil {
		return err
	}
	g := spec.gate()
	if err := g.refuse(); err != nil {
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

	provW := ui.BuildWriter()
	err = provider.Drive(ctx, spec.Config, provW, provW, trustFor(spec.Trust, ui), func(runner *provider.Runner) error {
		return body(ctx, runner, ui)
	})
	if err != nil {
		return fail(ctx, ui, err)
	}
	return nil
}

func (s Spec) gate() gate {
	return gate{
		command:     s.Command,
		class:       s.Consent,
		yes:         s.Yes,
		dry:         s.Dry,
		interactive: s.Interactive,
		unattended:  s.Unattended,
		in:          s.Stdin,
		out:         s.Stdout,
	}
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
