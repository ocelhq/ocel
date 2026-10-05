package link

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/ocelhq/ocel/cli/internal/terminal"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

func NewUnlinkCommand(dependencies Dependencies) *cobra.Command {
	return commands.DeclareMutating(commands.ReserveStdout(&cobra.Command{
		Use:     "unlink",
		Short:   "Unlink this directory from its console project",
		Example: "  $ ocel unlink",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			dir, err := projectDir(cmd.Context(), dependencies, cwd)
			if err != nil {
				return err
			}
			return runUnlink(dependencies, dir, cmd.OutOrStdout())
		},
	}))
}

func projectDir(ctx context.Context, dependencies Dependencies, cwd string) (string, error) {
	cfg, err := dependencies.LoadOptionalProject(ctx, cwd)
	if err != nil {
		return "", err
	}
	return cfg.Dir, nil
}

func runUnlink(dependencies Dependencies, projectDir string, stdout io.Writer) error {
	removed, err := console.DeleteLink(projectDir)
	if err != nil {
		return err
	}
	if dependencies.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, &resultv1.UnlinkResult{Unlinked: removed})
	}
	if !removed {
		fmt.Fprintln(stdout, "This directory isn't linked to a console project.")
		return nil
	}
	fmt.Fprintf(stdout, "%s Unlinked\n", terminal.PaletteFor(stdout).PassMark())
	return nil
}
