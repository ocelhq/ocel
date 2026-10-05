package telemetryflush

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/telemetry"
)

func NewCommand() *cobra.Command {
	flush := &cobra.Command{
		Use:                "flush",
		Hidden:             true,
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			telemetry.Flush(cmd.Context())
			return nil
		},
	}
	group := &cobra.Command{
		Use:               "telemetry",
		Hidden:            true,
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("unknown command %q for %q", cmd.Name(), cmd.Root().CommandPath())
		},
	}
	group.AddCommand(flush)
	return group
}
