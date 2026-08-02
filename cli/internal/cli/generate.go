package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/platform"
)

// generateCmd writes the files an app's own source resolves against — today,
// the client accessor `ocel/env/client` is mapped at. `ocel dev` and
// `ocel deploy` write them too, on their way to doing something else; this
// command is them alone, for the workflows that are neither.
//
// It reads declarations and never a value, so it needs no login, no provider
// session and no network. That is what makes it something a CI job can run
// before `tsc`, or a `postinstall` can run on a fresh clone — which is exactly
// where the accessor is otherwise missing, because the tsconfig entry pointing
// at it is committed and the file it names is not.
// collectDeclarations is a seam over deploycollector.Collect so tests can
// state what a discovery run declared without running one.
var collectDeclarations = deploycollector.Collect

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

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		return runGenerate(ctx, cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

// runGenerate declares the project's variables and writes what that alone
// determines. Discovery progress goes to stderr so stdout carries only the
// summary.
//
// The gate's verdict is deliberately not checked. A missing or invalid value
// is a reason not to deploy, not a reason to withhold a file whose content the
// declarations already fixed — refusing here would leave a developer unable to
// typecheck the very code they are editing to fix it.
func runGenerate(ctx context.Context, cwd string, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

	if err := platform.Ensure(cfg.Dir); err != nil {
		return err
	}

	gate := envgate.New(noValues{}, envgate.Scope{Apps: envApps(cfg)})
	if _, err := collectDeclarations(ctx, cfg, gate, stderr, stderr); err != nil {
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

// generateClientAccessors writes the client accessor into every app of the
// project, naming one flat set of keys. Which keys a browser may read is a
// declaration, and declarations are the project's, so every app's accessor
// names the same ones — the per-app divergence a deploy expresses is in the
// values behind them, which is a thing only a deploy resolves.
func generateClientAccessors(cfg *projectconfig.Config, keys []string) error {
	for _, plan := range appPlans(cfg, nil) {
		if err := clientenv.GenerateKeys(plan.dir, keys); err != nil {
			return err
		}
	}
	return nil
}

// noValues is a variable store holding nothing. Generation reads declarations
// and never a value — which keys an accessor names is settled by the
// declarations, and what each one holds is settled later, by the build that
// inlines it — so answering empty costs the generated file nothing and buys
// the command its whole point: it runs where no value could be resolved at
// all. A declaration whose value is missing is reported to the gate as a
// problem, which this command does not act on.
type noValues struct{}

func (noValues) List(context.Context) ([]envgate.Stored, error) { return nil, nil }

func (noValues) Reveal(context.Context, []envgate.Address) (map[envgate.Cell]string, error) {
	return nil, nil
}
