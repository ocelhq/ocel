package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/session"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/obs"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/node"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build your project's apps into .ocel/output without deploying",
	Long: "Build your project's apps into .ocel/output without deploying.\n\n" +
		"Express, Fastify and Hono apps are bundled, so only what the entrypoint imports\n" +
		"reaches the artifact: static directories, view templates and files read at run\n" +
		"time are left behind. Set OCEL_BUILD_PREFER_TRACING=1 to copy the dependency\n" +
		"tree instead, at the cost of a slower cold start.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		return runBuild(ctx, newSession(), cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func runBuild(ctx context.Context, d session.Session, cwd string, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	if err := node.Ensure(cfg.Dir); err != nil {
		return err
	}

	ctx, run, err := obs.Start(ctx, cfg.Dir, "ocel build")
	if err != nil {
		return err
	}
	defer run.Close()

	if err := d.BuildApp(ctx, cfg, nil, stderr); err != nil {
		return err
	}
	if err := clientenv.RecordUnresolved(cfg.Dir); err != nil {
		return err
	}

	functions, err := d.CollectAppFunctions(cfg.Dir)
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
