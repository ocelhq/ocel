package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/dev"
	"github.com/ocelhq/ocel/cli/internal/project"
)

var devReset bool

var devCmd = &cobra.Command{
	Use:   "dev -- <command> [args...]",
	Short: "Run your project in development mode",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		opts, err := devOptions(cmd.Context(), newDeps(), cwd, args, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		if err != nil {
			return err
		}
		return dev.Run(cmd.Context(), opts, devReset)
	},
}

func init() {
	devCmd.Flags().BoolVar(&devReset, "reset", false, "Wipe the data this project's dev resources have kept, then start from empty ones")
}

func devOptions(ctx context.Context, deps cmddeps.Deps, cwd string, command []string, stdout, stderr io.Writer, stdin io.Reader) (dev.Options, error) {
	cfg, err := project.ResolveOptional(ctx, cwd, explicitConfigPath())
	if err != nil {
		return dev.Options{}, err
	}
	return dev.Options{
		Project:         cfg,
		Command:         command,
		OpenDocker:      deps.OpenDocker,
		Stdin:           stdin,
		Stdout:          stdout,
		Stderr:          stderr,
		StdinIsTerminal: deps.StdinIsTerminal(stdin),
	}, nil
}
