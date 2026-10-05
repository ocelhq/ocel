package telemetryflush

import (
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/telemetry"
)

func NewCommand() *cobra.Command {
	flush := &cobra.Command{
		Use:                "flush",
		Hidden:             true,
		Args:               cobra.ArbitraryArgs,
		PersistentPreRunE:  func(*cobra.Command, []string) error { return nil },
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			telemetry.Flush(cmd.Context())
			return nil
		},
	}
	group := &cobra.Command{Use: "telemetry", Hidden: true}
	group.AddCommand(flush)
	return group
}
