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
	"github.com/ocelhq/ocel/cli/internal/cli/preflight"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build every app in your project without deploying",
	Long: "Build every app in your project without deploying: a serverless app's functions\n" +
		"into " + buildoutput.Dir + ", and a container app's image into the local docker daemon.\n" +
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

		return runBuild(cmd.Context(), newDeps(), cwd)
	},
}

func runBuild(ctx context.Context, deps cmddeps.Deps, cwd string) (err error) {
	declared, err := deps.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	if declared.Provider == nil {
		if _, err := declared.ResolveDeclaredComputes(); err != nil {
			return fmt.Errorf("%w: give each a `compute` under `apps`, or name the provider in %s", err, filepath.Base(declared.Path))
		}
	}

	hasJS, err := build.HasJS(declared)
	if err != nil {
		return err
	}
	if hasJS {
		if err := node.Ensure(declared.Dir); err != nil {
			return err
		}
	}

	ctx, building, err := deps.Events.Begin(ctx, "ocel build", declared.Dir)
	if err != nil {
		return err
	}
	defer building.End(&err)
	cfg, err := resolveBuiltComputes(ctx, deps, building, declared)
	if err != nil {
		return err
	}
	phase := building.Phase(progressv1.Phase_PHASE_BUILD)

	clients := builtInClients(cfg, appurl.Production(cfg))
	built, err := deps.BuildApps(run.ContextWithSpan(ctx, phase), cfg, build.Env(clients), declaredArchs(cfg), appBuildLog(phase))
	if err != nil {
		return err
	}
	if err := clientenv.Record(cfg.Dir, clients); err != nil {
		return err
	}
	phase.End(nil)
	for _, line := range builtLines(built) {
		phase.Say(line)
	}
	building.Succeed(builtHeadline(built))
	return nil
}

func resolveBuiltComputes(ctx context.Context, deps cmddeps.Deps, building *run.Run, declared *project.Project) (resolved *project.Project, err error) {
	if len(declared.UnresolvedApps()) == 0 {
		return declared.ResolveDeclaredComputes()
	}
	check := building.Phase(progressv1.Phase_PHASE_CHECK)
	defer func() { check.End(err) }()
	prov, err := providerclient.Start(ctx, declared, check, deps.Questions, providerclient.PinToLock)
	if err != nil {
		return nil, err
	}
	defer prov.Close()
	return preflight.ResolveComputesFromProvider(ctx, prov, declared)
}

func appBuildLog(phase *run.Span) build.Log {
	return build.Log{
		Shared: phase.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED),
		Unit: func(app string) (io.Writer, func(error)) {
			unit := phase.Unit(app, progress.Building.Title("app "+app))
			return unit.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED), unit.End
		},
	}
}

func declaredArchs(cfg *project.Project) map[string]string {
	archs := map[string]string{}
	for _, a := range build.ImageApps(cfg.Apps) {
		archs[a.Name] = ""
		if a.Arch != "" {
			archs[a.Name], _ = arch.GoArch(a.Arch)
		}
	}
	return archs
}

func builtHeadline(built build.Output) string {
	if len(built.Functions) == 0 && len(built.Images) > 0 {
		return fmt.Sprintf("Built %d %s", len(built.Images), plural(len(built.Images), "image", "images"))
	}
	return fmt.Sprintf("Built %d %s into %s", len(built.Functions), plural(len(built.Functions), "function", "functions"), buildoutput.Dir)
}

func builtLines(built build.Output) []string {
	lines := make([]string, 0, len(built.Images))
	for _, app := range slices.Sorted(maps.Keys(built.Images)) {
		lines = append(lines, fmt.Sprintf("Built the image of %s as %s", app, built.Images[app]))
	}
	return lines
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func builtInClients(cfg *project.Project, urls map[string]string) []clientenv.App {
	apps := make([]clientenv.App, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		dir := filepath.Join(cfg.Dir, a.Path)
		bundle := language.HasClientBundle(a.Framework(), dir)
		apps = append(apps, clientenv.App{
			Name:         a.Name,
			Dir:          dir,
			ClientBundle: bundle,
			Variables:    appurl.Variables(bundle, urls[a.Name]),
		})
	}
	return apps
}
