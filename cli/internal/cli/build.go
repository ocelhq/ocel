package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/appurl"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/constants"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build your project's apps into " + constants.ProjectStateDirName + "/output without deploying",
	Long: "Build your project's apps into " + constants.ProjectStateDirName + "/output without deploying.\n\n" +
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

		return runBuild(ctx, newDeps(), cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func runBuild(ctx context.Context, deps cmddeps.Deps, cwd string, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	holdsJS, err := discovery.HoldsJS(cfg)
	if err != nil {
		return err
	}
	if holdsJS {
		if err := node.Ensure(cfg.Dir); err != nil {
			return err
		}
	}

	ctx, run, err := runtrace.Start(ctx, cfg.Dir, "ocel build")
	if err != nil {
		return err
	}
	defer run.Close()

	urls := appurl.Production(cfg)
	if err := deps.BuildApp(ctx, cfg, appurl.BuildEnv(cfg, urls), stderr); err != nil {
		return err
	}
	if err := clientenv.Record(cfg.Dir, builtInClients(cfg, urls)); err != nil {
		return err
	}

	functions, err := deps.CollectAppFunctions(cfg.Dir)
	if err != nil {
		return err
	}

	noun := "functions"
	if len(functions) == 1 {
		noun = "function"
	}
	fmt.Fprintf(stdout, "Built %d %s into %s/output\n", len(functions), noun, constants.ProjectStateDirName)
	return nil
}

func builtInClients(cfg *projectconfig.Config, urls map[string]string) []clientenv.App {
	if len(cfg.Apps) == 0 {
		bundle := discovery.ClientBundle(envwire.RootRuntime, cfg.Dir)
		return []clientenv.App{{Dir: cfg.Dir, ClientBundle: bundle, Variables: appurl.Variables(bundle, urls[envwire.RootApp])}}
	}
	apps := make([]clientenv.App, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		dir := filepath.Join(cfg.Dir, a.Path)
		bundle := discovery.ClientBundle(a.Runtime.Name, dir)
		apps = append(apps, clientenv.App{
			Name:         a.Name,
			Dir:          dir,
			ClientBundle: bundle,
			Variables:    appurl.Variables(bundle, urls[a.Name]),
		})
	}
	return apps
}
