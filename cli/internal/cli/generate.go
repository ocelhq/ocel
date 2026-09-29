package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
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

		return runGenerate(cmd.Context(), newDeps(), cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
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

	declarations := variables.NewDeclarations(noValues{}, variables.Scope{Apps: variablescope.Apps(cfg)})
	if _, err := deps.CollectDeclarations(ctx, cfg, declarations, stderr, stderr); err != nil {
		return err
	}

	keys, err := clientenv.Declared(declarations.Definitions())
	if err != nil {
		return err
	}
	named, err := clientenv.GenerateProjectAccessors(cfg, keys)
	if err != nil {
		return err
	}

	noun := "variables"
	if named == 1 {
		noun = "variable"
	}
	fmt.Fprintf(stdout, "Generated the client accessor for %d client-accessible %s\n", named, noun)
	return nil
}

type noValues struct{}

func (noValues) List(context.Context) ([]variables.ValueMetadata, error) { return nil, nil }

func (noValues) Reveal(context.Context, []variables.Coordinate) (map[variables.Coordinate]string, error) {
	return nil, nil
}
