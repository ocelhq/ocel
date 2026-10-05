package lock

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/lockfile"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

type Dependencies struct {
	commands.Invocation
	Pin func(ctx context.Context, projectDir string) (lockfile.Lock, error)
}

func NewCommand(dependencies Dependencies) *cobra.Command {
	return commands.DeclareMutating(commands.ReserveStdout(&cobra.Command{
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
			return runLock(cmd.Context(), dependencies, cwd, cmd.OutOrStdout())
		},
	}))
}

func runLock(ctx context.Context, dependencies Dependencies, cwd string, stdout io.Writer) error {
	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	pinned, err := dependencies.Pin(ctx, cfg.Dir)
	if err != nil {
		return err
	}
	if dependencies.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, &resultv1.LockResult{
			Path:       lockfile.Path(cfg.Dir),
			CliVersion: pinned.CLI,
			Providers:  pinnedExecutables(pinned.Providers),
			Connectors: pinnedExecutables(pinned.Connectors),
		})
	}
	fmt.Fprintln(stdout, lockfile.Path(cfg.Dir))
	return nil
}

func pinnedExecutables(pins map[string]map[string]string) []*resultv1.PinnedExecutable {
	executables := make([]*resultv1.PinnedExecutable, 0, len(pins))
	for _, name := range slices.Sorted(maps.Keys(pins)) {
		executables = append(executables, &resultv1.PinnedExecutable{Name: name, Digests: pins[name]})
	}
	return executables
}
