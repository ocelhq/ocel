package lock

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/lockfile"
)

func NewCommand(invocation commands.Invocation) *cobra.Command {
	return commands.Mutates(&cobra.Command{
		Use:   "lock",
		Short: "Pin the provider binaries this CLI version runs",
		Long: "Pin the provider binaries this CLI version runs.\n\n" +
			"Writes " + lockfile.Name + " beside the project config, recording this CLI's version and the " +
			"sha256 of every provider archive of that release on every platform. Commit it: every machine " +
			"and every CI run then fetches the same bytes. Run it again after changing CLI version.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			ctx := cmd.Context()

			cfg, err := invocation.LoadProject(ctx, cwd)
			if err != nil {
				return err
			}
			if err := executables.Pin(ctx, cfg.Dir); err != nil {
				return err
			}

			fmt.Fprintln(cmd.OutOrStdout(), lockfile.Path(cfg.Dir))
			return nil
		},
	})
}
