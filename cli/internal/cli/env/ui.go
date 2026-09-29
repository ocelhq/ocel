package env

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/valuestore"
	"github.com/ocelhq/ocel/cli/internal/variableeditor"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
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
		declarations, err := discoverVariables(ctx, cfg, prov, opts, run)
		if err != nil {
			return err
		}

		editor, err := serveAndOpenEditor(deps, ctx, cfg, prov, opts.tier(), declarations, stdin, stdout)
		if err != nil {
			return err
		}
		defer editor.Close()
		err = editor.Wait(ctx)
		if errors.Is(err, variableeditor.ErrAbandoned) {
			fmt.Fprintln(stdout, "The editor closed.")
			return nil
		}
		return err
	})
}

func serveAndOpenEditor(
	deps cmddeps.Deps,
	ctx context.Context,
	cfg *projectconfig.Config,
	prov *providerclient.Provider,
	tier environmentv1.Tier,
	declarations *variables.Declarations,
	stdin io.Reader,
	stdout io.Writer,
) (*variableeditor.Session, error) {
	editor, err := deps.ServeVariableEditor(ctx, cfg, prov, tier, declarations, nil)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(stdout, "\nVariables for %s are at:\n\n  %s\n\n", cfg.Slug, editor.URL)
	if !deps.BrowserReachable(stdin) {
		return editor, nil
	}
	if err := deps.OpenBrowser(editor.URL); err != nil {
		fmt.Fprintln(stdout, "Couldn't open your browser automatically — open the link above manually.")
	}
	return editor, nil
}

func discoverVariables(ctx context.Context, cfg *projectconfig.Config, prov *providerclient.Provider, opts envOptions, run *events.Run) (*variables.Declarations, error) {
	declarations := projectDeclarations(cfg, prov, opts)
	err := collecting(run, cfg, func(output io.Writer) error {
		_, err := deploycollector.PrepareAndCollect(ctx, cfg, declarations, io.Discard, output)
		return err
	})
	if err != nil {
		return nil, err
	}
	return declarations, nil
}

func projectDeclarations(cfg *projectconfig.Config, prov *providerclient.Provider, opts envOptions) *variables.Declarations {
	return variables.NewDeclarations(valuestore.Store{
		Provider: prov,
		Config:   cfg,
		Tier:     opts.tier(),
	}, variablescope.Of(cfg, opts.tier(), ""))
}
