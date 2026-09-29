package dev

import (
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/dev"
)

func NewRunCommand(dependencies Dependencies) *cobra.Command {
	return commands.ShareTerminalWithChild(&cobra.Command{
		Use:   "run -- <command> [args...]",
		Short: "Run a one-off command with your project's resource connections",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOnBus(cmd, dependencies, "ocel run", args, dev.RunOnce)
		},
	})
}
