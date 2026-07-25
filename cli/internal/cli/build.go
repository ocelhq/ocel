package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

// buildCmd builds the project's apps into .ocel/output without deploying
// anything. It talks to no API and needs neither a login nor a configured
// provider, so a CI job can build in a container holding no credentials and
// deploy from the same checkout later with `--prebuilt`.
var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build your project's apps into .ocel/output without deploying",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		return runBuild(ctx, cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

// runBuild resolves the project config, builds its apps, and reports what
// landed in the output. Builder progress goes to stderr so stdout carries only
// the summary.
func runBuild(ctx context.Context, cwd string, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

	if err := buildApp(ctx, cfg, stderr); err != nil {
		return err
	}

	functions, err := collectAppFunctions(cfg.Dir)
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
