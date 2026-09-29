package cli

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/appurl"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/arch"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build every app in your project without deploying",
	Long: "Build every app in your project without deploying: a serverless app's functions\n" +
		"into " + appbuild.ArtifactRootDir + ", and a container app's image into the local docker daemon.\n" +
		"`ocel deploy --prebuilt` deploys what this built.\n\n" +
		"Express, Fastify and Hono servers are bundled, so only what the entrypoint imports\n" +
		"reaches the function: static directories, view templates and files read at run\n" +
		"time are left behind. Set OCEL_BUILD_PREFER_TRACING=1 to copy the dependency\n" +
		"tree instead.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		return runBuild(cmd.Context(), newDeps(), cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func runBuild(ctx context.Context, deps cmddeps.Deps, cwd string, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}
	for _, a := range build.FunctionApps(cfg.Apps) {
		if a.Framework.Missing != nil {
			return a.Framework.Missing
		}
	}

	hasJS, err := build.HasJS(cfg)
	if err != nil {
		return err
	}
	if hasJS {
		if err := node.Ensure(cfg.Dir); err != nil {
			return err
		}
	}

	ctx, run, err := runtrace.Start(ctx, cfg.Dir, "ocel build")
	if err != nil {
		return err
	}
	defer run.Close()

	clients := builtInClients(cfg, appurl.Production(cfg))
	built, err := deps.BuildApps(ctx, cfg, build.Env(clients), declaredArchs(cfg), build.Log{Shared: stderr})
	if err != nil {
		return err
	}
	if err := clientenv.Record(cfg.Dir, clients); err != nil {
		return err
	}
	reportBuilt(stdout, built)
	return nil
}

func declaredArchs(cfg *projectconfig.Config) map[string]string {
	archs := map[string]string{}
	for _, a := range build.ImageApps(cfg.Apps) {
		archs[a.Name] = ""
		if a.Framework.Arch != "" {
			archs[a.Name], _ = arch.GoArch(a.Framework.Arch)
		}
	}
	return archs
}

func reportBuilt(stdout io.Writer, built build.Output) {
	if len(built.Functions) > 0 || len(built.Images) == 0 {
		noun := "functions"
		if len(built.Functions) == 1 {
			noun = "function"
		}
		fmt.Fprintf(stdout, "Built %d %s into %s\n", len(built.Functions), noun, appbuild.ArtifactRootDir)
	}
	for _, app := range slices.Sorted(maps.Keys(built.Images)) {
		fmt.Fprintf(stdout, "Built the image of %s as %s\n", app, built.Images[app])
	}
}

func builtInClients(cfg *projectconfig.Config, urls map[string]string) []clientenv.App {
	if len(cfg.Apps) == 0 {
		bundle := language.HasClientBundle(appbuild.FrameworkNode, cfg.Dir)
		return []clientenv.App{{Dir: cfg.Dir, ClientBundle: bundle, Variables: appurl.Variables(bundle, urls[variablescope.RootApp])}}
	}
	apps := make([]clientenv.App, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		dir := filepath.Join(cfg.Dir, a.Path)
		bundle := language.HasClientBundle(a.Framework.Name, dir)
		apps = append(apps, clientenv.App{
			Name:         a.Name,
			Dir:          dir,
			ClientBundle: bundle,
			Variables:    appurl.Variables(bundle, urls[a.Name]),
		})
	}
	return apps
}
