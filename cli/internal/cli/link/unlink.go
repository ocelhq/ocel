package link

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	consolelink "github.com/ocelhq/ocel/cli/internal/console/link"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func NewUnlinkCommand(deps cmddeps.Deps) *cobra.Command {
	return &cobra.Command{
		Use:     "unlink",
		Short:   "Unlink this directory from its console project",
		Example: "  $ ocel unlink",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			dir, err := projectDir(cmd.Context(), deps, cwd)
			if err != nil {
				return err
			}
			return runUnlink(dir, cmd.OutOrStdout())
		},
	}
}

func projectDir(ctx context.Context, deps cmddeps.Deps, cwd string) (string, error) {
	cfg, err := projectconfig.ResolveOptional(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return "", err
	}
	return cfg.Dir, nil
}

func runUnlink(projectDir string, stdout io.Writer) error {
	removed, err := consolelink.Clear(projectDir)
	if err != nil {
		return err
	}
	if !removed {
		fmt.Fprintln(stdout, "This directory isn't linked to a console project.")
		return nil
	}
	fmt.Fprintf(stdout, "%s Unlinked\n", check)
	return nil
}
