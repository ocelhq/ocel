package providerui

import (
	"context"
	"io"

	"github.com/ocelhq/ocel/cli/internal/cli/session"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
)

func Run(ctx context.Context, sess session.Session, cfg *projectconfig.Config, command string, stdout io.Writer, fn func(context.Context, *provider.Runner, *deployui.Session) error) error {
	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}

	ctx, run, err := runtrace.Start(ctx, cfg.Dir, command)
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sess.Format(), sess.Verbose())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = provider.Drive(ctx, cfg, provW, provW, func(runner *provider.Runner) error {
		return fn(ctx, runner, ui)
	})
	if err != nil {
		return fail(ctx, ui, err)
	}
	return nil
}

func fail(ctx context.Context, ui *deployui.Session, err error) error {
	if ctx.Err() != nil {
		ui.Cancel()
		return &exitsig.ExitError{Code: exitsig.InterruptCode}
	}
	ui.Fail(err)
	return &exitsig.ExitError{Code: 1}
}
