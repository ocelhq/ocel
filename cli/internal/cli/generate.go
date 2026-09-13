package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
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

	gate := envgate.New(noValues{}, envgate.Scope{Apps: envwire.Apps(cfg)})
	if _, err := deps.CollectDeclarations(ctx, cfg, gate, stderr, stderr); err != nil {
		return err
	}

	keys, err := clientenv.Declared(gate.Definitions())
	if err != nil {
		return err
	}
	named, err := generateClientAccessors(cfg, keys)
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

func generateClientAccessors(cfg *projectconfig.Config, keys []clientenv.Key) (int, error) {
	apps := []clientenv.App{{Dir: cfg.Dir, Runtime: envwire.RootRuntime}}
	if len(cfg.Apps) > 0 {
		apps = apps[:0]
		for _, a := range cfg.Apps {
			apps = append(apps, clientenv.App{Name: a.Name, Dir: filepath.Join(cfg.Dir, a.Path), Runtime: a.Runtime.Name})
		}
	}
	named := 0
	for _, app := range apps {
		if err := generateClientAccessor(cfg.Dir, app, keys); err != nil {
			return 0, err
		}
		named = max(named, len(clientenv.Offered(keys, app.Runtime)))
	}
	return named, nil
}

func generateClientAccessor(projectDir string, app clientenv.App, keys []clientenv.Key) error {
	if err := clientenv.GenerateKeys(projectDir, app, keys); err != nil {
		return err
	}
	return clientenv.PointAppImports(projectDir, app)
}

type noValues struct{}

func (noValues) List(context.Context) ([]envgate.Stored, error) { return nil, nil }

func (noValues) Reveal(context.Context, []envgate.Address) (map[envgate.Cell]string, error) {
	return nil, nil
}
