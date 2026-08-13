package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/node"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build your project's apps into .ocel/output without deploying",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		return runBuild(ctx, defaultDeps(), cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func runBuild(ctx context.Context, d deps, cwd string, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd)
	if err != nil {
		return err
	}

	if err := node.Ensure(cfg.Dir); err != nil {
		return err
	}

	ctx, run, err := startRun(ctx, cfg, "ocel build")
	if err != nil {
		return err
	}
	defer run.Close()

	if err := d.buildApp(ctx, cfg, nil, stderr); err != nil {
		return err
	}
	if err := clientenv.RecordUnresolved(cfg.Dir); err != nil {
		return err
	}

	functions, err := d.collectAppFunctions(cfg.Dir)
	if err != nil {
		return err
	}

	noun := "functions"
	if len(functions) == 1 {
		noun = "function"
	}
	fmt.Fprintf(stdout, "Built %d %s into .ocel/output\n", len(functions), noun)
	return nil
}
