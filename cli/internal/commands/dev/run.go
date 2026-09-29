package dev

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/dev"
)

func NewRunCommand(deps cmddeps.Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "run -- <command> [args...]",
		Short: "Run a one-off command with your project's resource connections",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			opts, err := loadOptions(cmd.Context(), deps, cwd, args, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
			if err != nil {
				return err
			}
			return dev.RunOnce(cmd.Context(), opts, cwd)
		},
	}
}
