package env

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/varsui"
)

func newUICommand(deps cmddeps.Deps) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "ui",
		Short:   "Open the variable editor",
		Example: "  $ ocel env ui\n  $ ocel env ui --preview",
		Args:    cobra.NoArgs,
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return withCommand(cmd, deps, func(ctx context.Context, cwd string) error {
			return runEnvUI(ctx, deps, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	previewFlag(cmd, &opts)
	return cmd
}

func runEnvUI(ctx context.Context, deps cmddeps.Deps, cwd string, opts envOptions, stdout, stderr io.Writer) error {
	return withEnvProvider(ctx, deps, cwd, opts, stderr, func(runner *provider.Runner, cfg *projectconfig.Config) error {
		gate, err := discoverVariables(ctx, cfg, runner, opts, stderr)
		if err != nil {
			return err
		}

		varsSession, err := serveAndOpenVarsUI(deps, ctx, cfg, runner, opts.preview, gate, stdout)
		if err != nil {
			return err
		}
		defer varsSession.Close()
		return varsSession.Wait(ctx)
	})
}

func serveAndOpenVarsUI(
	deps cmddeps.Deps,
	ctx context.Context,
	cfg *projectconfig.Config,
	runner *provider.Runner,
	preview bool,
	gate *envgate.Gate,
	stdout io.Writer,
) (*varsui.Session, error) {
	varsSession, err := deps.ServeVarsUI(ctx, cfg, runner, preview, gate)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(stdout, "\nVariables for %s are at:\n\n  %s\n\n", cfg.Slug, varsSession.URL)
	if err := deps.OpenBrowser(varsSession.URL); err != nil {
		fmt.Fprintln(stdout, "Couldn't open your browser automatically — open the link above manually.")
	}
	return varsSession, nil
}

func discoverVariables(ctx context.Context, cfg *projectconfig.Config, runner *provider.Runner, opts envOptions, stderr io.Writer) (*envgate.Gate, error) {
	gate := envGate(cfg, runner, opts)
	if _, err := deploycollector.Collect(ctx, cfg, gate, io.Discard, stderr); err != nil {
		return nil, err
	}
	return gate, nil
}

func envGate(cfg *projectconfig.Config, runner *provider.Runner, opts envOptions) *envgate.Gate {
	return envgate.New(envwire.Values{
		Runner: runner,
		Slug:   cfg.Slug,
		Tier:   envTier(opts),
	}, envwire.Scope(cfg, opts.preview, ""))
}
