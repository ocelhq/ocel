package env

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
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
			return runEnvUI(ctx, deps, cwd, opts, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	previewFlag(cmd, &opts)
	return cmd
}

func runEnvUI(ctx context.Context, deps cmddeps.Deps, cwd string, opts envOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	return withEnvProviderSealing(ctx, deps, cwd, opts, "ocel env ui", stdin, stderr, func(ctx context.Context, run *events.Run, prov *providerclient.Provider, cfg *projectconfig.Config, _ *contractv1.PreflightResponse) error {
		gate, err := discoverVariables(ctx, cfg, prov, opts, run)
		if err != nil {
			return err
		}

		varsSession, err := serveAndOpenVarsUI(deps, ctx, cfg, prov, opts.preview, gate, stdin, stdout)
		if err != nil {
			return err
		}
		defer varsSession.Close()
		err = varsSession.Wait(ctx)
		if errors.Is(err, varsui.ErrAbandoned) {
			fmt.Fprintln(stdout, "The editor closed.")
			return nil
		}
		return err
	})
}

func serveAndOpenVarsUI(
	deps cmddeps.Deps,
	ctx context.Context,
	cfg *projectconfig.Config,
	prov *providerclient.Provider,
	preview bool,
	gate *envgate.Gate,
	stdin io.Reader,
	stdout io.Writer,
) (*varsui.Session, error) {
	varsSession, err := deps.ServeVarsUI(ctx, cfg, prov, preview, gate, nil)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(stdout, "\nVariables for %s are at:\n\n  %s\n\n", cfg.Slug, varsSession.URL)
	if !deps.BrowserReachable(stdin) {
		return varsSession, nil
	}
	if err := deps.OpenBrowser(varsSession.URL); err != nil {
		fmt.Fprintln(stdout, "Couldn't open your browser automatically — open the link above manually.")
	}
	return varsSession, nil
}

func discoverVariables(ctx context.Context, cfg *projectconfig.Config, prov *providerclient.Provider, opts envOptions, run *events.Run) (*envgate.Gate, error) {
	gate := envGate(cfg, prov, opts)
	err := collecting(run, cfg, func(output io.Writer) error {
		_, err := deploycollector.PrepareAndCollect(ctx, cfg, gate, io.Discard, output)
		return err
	})
	if err != nil {
		return nil, err
	}
	return gate, nil
}

func envGate(cfg *projectconfig.Config, prov *providerclient.Provider, opts envOptions) *envgate.Gate {
	return envgate.New(envwire.Values{
		Provider: prov,
		Slug:     cfg.Slug,
		Tier:     envTier(opts),
	}, envwire.Scope(cfg, opts.preview, ""))
}
