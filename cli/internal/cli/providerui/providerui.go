package providerui

import (
	"context"
	"io"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
)

func Run(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, command string, stdout io.Writer, fn func(context.Context, *provider.Runner, *deployui.Session) error) error {
	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}

	ctx, run, err := runtrace.Start(ctx, cfg.Dir, command)
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, deps.Format(), deps.Verbose())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = provider.Drive(ctx, cfg, provW, provW, trustFor(deps, ui), func(runner *provider.Runner) error {
		return fn(ctx, runner, ui)
	})
	if err != nil {
		return fail(ctx, ui, err)
	}
	return nil
}

func trustFor(deps cmddeps.Deps, ui *deployui.Session) provider.Trust {
	trust := deps.HostTrust
	trust.Suspend = ui.Suspend
	return trust
}

func fail(ctx context.Context, ui *deployui.Session, err error) error {
	if ctx.Err() != nil {
		ui.Cancel()
		return &exitsig.ExitError{Code: exitsig.InterruptCode}
	}
	ui.Fail(err)
	return &exitsig.ExitError{Code: 1}
}
