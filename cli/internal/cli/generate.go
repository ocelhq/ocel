package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/node"
)

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate the app-side files ocel derives from your declarations",
	Long: "Generate the app-side files ocel derives from your declarations.\n\n" +
		"Writes each app's client accessor and points that app's 'ocel/env/client' imports at it, " +
		"which `ocel dev` and `ocel deploy` also do. It reads declarations only — no login, no provider " +
		"and no network — so it can run in CI before a typecheck, or from a postinstall on a fresh clone.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		return runGenerate(ctx, newDeps(), cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func runGenerate(ctx context.Context, deps cmddeps.Deps, cwd string, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	if err := node.Ensure(cfg.Dir); err != nil {
		return err
	}

	gate := envgate.New(noValues{}, envgate.Scope{Apps: envApps(cfg)})
	if _, err := deps.CollectDeclarations(ctx, cfg, gate, stderr, stderr); err != nil {
		return err
	}

	keys := clientenv.DeclaredKeys(gate.Definitions())
	if err := generateClientAccessors(cfg, keys); err != nil {
		return err
	}

	if len(keys) == 0 {
		fmt.Fprintln(stdout, "No client-accessible variables declared; nothing to generate")
		return nil
	}
	noun := "variables"
	if len(keys) == 1 {
		noun = "variable"
	}
	fmt.Fprintf(stdout, "Generated the client accessor for %d client-accessible %s\n", len(keys), noun)
	return nil
}

func generateClientAccessors(cfg *projectconfig.Config, keys []string) error {
	for _, plan := range appPlans(cfg, nil) {
		if err := clientenv.GenerateKeys(plan.dir, keys); err != nil {
			return err
		}
	}
	return nil
}

type noValues struct{}

func (noValues) List(context.Context) ([]envgate.Stored, error) { return nil, nil }

func (noValues) Reveal(context.Context, []envgate.Address) (map[envgate.Cell]string, error) {
	return nil, nil
}
