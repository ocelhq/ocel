package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/lockfile"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
)

var lockCmd = &cobra.Command{
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

		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
		if err != nil {
			return err
		}
		if err := provider.Pin(ctx, cfg.Dir); err != nil {
			return err
		}

		fmt.Fprintln(cmd.OutOrStdout(), lockfile.Path(cfg.Dir))
		return nil
	},
}
