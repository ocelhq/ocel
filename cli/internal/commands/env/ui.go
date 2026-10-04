package env

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variableeditor"
	"github.com/ocelhq/ocel/cli/internal/variables"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func newUICommand(dependencies Dependencies) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "ui",
		Short:   "Open the variable editor",
		Example: "  $ ocel env ui\n  $ ocel env ui --preview",
		Args:    cobra.NoArgs,
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return withCommand(cmd, dependencies, func(ctx context.Context, cwd string) error {
			return runEnvUI(ctx, dependencies, cwd, opts, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	previewFlag(cmd, &opts)
	return commands.Mutates(cmd)
}

func runEnvUI(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	return withEnvProviderOfferingVariablesKey(ctx, dependencies, cwd, opts, "ocel env ui", stdin, stderr, func(ctx context.Context, run *run.Run, provider *providerprocess.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		declarations, err := discoverVariables(ctx, cfg, provider, opts, run)
		if err != nil {
			return err
		}

		editor, err := serveAndOpenEditor(dependencies, ctx, cfg, provider, opts.tier(), declarations, stdin, stdout)
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
	dependencies Dependencies,
	ctx context.Context,
	cfg *project.Project,
	provider *providerprocess.Provider,
	tier environmentv1.Tier,
	declarations *variables.Declarations,
	stdin io.Reader,
	stdout io.Writer,
) (*variableeditor.Session, error) {
	editor, err := dependencies.ServeVariableEditor(ctx, cfg, provider, tier, declarations, nil)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(stdout, "\nVariables for %s are at:\n\n  %s\n\n", cfg.Slug, editor.URL)
	if !dependencies.IsBrowserReachable(stdin) {
		return editor, nil
	}
	if err := dependencies.OpenBrowser(editor.URL); err != nil {
		fmt.Fprintln(stdout, "Couldn't open your browser automatically — open the link above manually.")
	}
	return editor, nil
}
