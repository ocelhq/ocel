package build

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/workdir"

	"github.com/ocelhq/ocel/cli/internal/english"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/appurl"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type Dependencies struct {
	commands.Invocation
	BuildApps           func(ctx context.Context, cfg *project.Project, variables map[string]build.AppVariables, archs map[string]string, workers build.HostedWorkers, host build.Host, log build.Log) (build.Output, error)
	CollectDeclarations func(ctx context.Context, cfg *project.Project, declarations *variables.Declarations, stdout, stderr io.Writer) ([]declaration.Resource, error)
}

func NewCommand(dependencies Dependencies) *cobra.Command {
	return commands.DeclareRunEvents(commands.DeclareMutating(&cobra.Command{
		Use:   "build",
		Short: "Build every app in your project without deploying",
		Long: "Build every app in your project without deploying: a serverless app's functions\n" +
			"into " + buildoutput.Dir + ", and a container app's image into the local docker daemon.\n" +
			"`ocel deploy --prebuilt` deploys what this built.\n\n" +
			"Express, Fastify, Hono and compiled NestJS servers are bundled, so only what the\n" +
			"entrypoint imports reaches the function: static directories, view templates and\n" +
			"files read at run time are left behind. Set OCEL_BUILD_PREFER_TRACING=1 to copy\n" +
			"the dependency tree instead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			return runBuild(cmd.Context(), dependencies, cwd)
		},
	}))
}

func runBuild(ctx context.Context, dependencies Dependencies, cwd string) (err error) {
	declared, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	if declared.Provider == nil {
		if _, err := declared.ResolveDeclaredComputes(); err != nil {
			return fmt.Errorf("%w: give each a `compute` under `apps`, or name the provider in %s", err, filepath.Base(declared.Path))
		}
	}

	ctx, building, err := dependencies.Events.Begin(ctx, "ocel build", declared.Dir)
	if err != nil {
		return err
	}
	defer building.End(&err)
	cfg, host, err := resolveBuild(ctx, dependencies, building, declared)
	if err != nil {
		return err
	}
	phase := building.Phase(progressv1.Phase_PHASE_BUILD)

	declarations, workers, err := collectDeclarations(ctx, dependencies, cfg, phase)
	if err != nil {
		return err
	}
	clients := variables.ClientAppsOf(cfg, declarations.Definitions(), builtInValues(cfg, appurl.FormatProductionURLs(cfg)))
	releaseOutput, err := workdir.LockBuildOutput(ctx, cfg.Dir, func() {
		phase.Say("Waiting for another ocel run in " + cfg.Dir + " to finish with " + buildoutput.Dir)
	})
	if err != nil {
		return err
	}
	defer releaseOutput()
	built, err := dependencies.BuildApps(run.ContextWithSpan(ctx, phase), cfg, build.SplitVariablesByClass(clients, nil), declaredArchs(cfg), workers, host, appBuildLog(phase))
	if err != nil {
		return err
	}
	if err := variables.RecordClientValues(cfg.Dir, clients); err != nil {
		return err
	}
	phase.End(nil)
	for _, line := range builtLines(built) {
		phase.Say(line)
	}
	building.Succeed(builtHeadline(built))
	return nil
}

func collectDeclarations(ctx context.Context, dependencies Dependencies, cfg *project.Project, phase *run.Span) (*variables.Declarations, build.HostedWorkers, error) {
	said := phase.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED)
	declarations := variables.NewDeclarations(variables.NoValues{}, variables.Scope{Apps: variables.AppsOf(cfg)})
	resources, err := dependencies.CollectDeclarations(ctx, cfg, declarations, said, said)
	if err != nil {
		return nil, nil, err
	}
	placement, err := manifest.PlaceConsumers(cfg, resources)
	if err != nil {
		return nil, nil, err
	}
	return declarations, placement.HostedWorkers(), nil
}

func resolveBuild(ctx context.Context, dependencies Dependencies, building *run.Run, declared *project.Project) (resolved *project.Project, host build.Host, err error) {
	unresolved := len(declared.UnresolvedApps()) > 0
	if !unresolved {
		resolved, err = declared.ResolveDeclaredComputes()
		if err != nil || len(build.FindNextFunctionApps(resolved.Apps)) == 0 {
			return resolved, build.Host{}, err
		}
	}
	check := building.Phase(progressv1.Phase_PHASE_CHECK)
	defer func() { check.End(err) }()
	provider, _, err := dependencies.OpenProvider(ctx, check, declared, commands.OpenOptions{})
	if err != nil {
		return nil, build.Host{}, err
	}
	defer provider.Close()
	if unresolved {
		resolved, err = readiness.ResolveComputes(provider, declared)
		if err != nil {
			return nil, build.Host{}, err
		}
	}
	return resolved, build.ReadHost(provider.Facts()), nil
}

func appBuildLog(phase *run.Span) build.Log {
	return build.Log{
		Shared: phase.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED),
		AppLog: func(app string) (io.Writer, func(error)) {
			span := phase.Child(app, progress.Building.Title("app "+app))
			return span.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED), span.End
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
		return "Built " + english.Count(len(built.Images), "image")
	}
	return "Built " + english.Count(len(built.Functions), "function") + " into " + buildoutput.Dir
}

func builtLines(built build.Output) []string {
	lines := make([]string, 0, len(built.Images))
	for _, app := range slices.Sorted(maps.Keys(built.Images)) {
		lines = append(lines, fmt.Sprintf("Built the image of %s as %s", app, built.Images[app]))
	}
	return lines
}

func builtInValues(cfg *project.Project, urls map[string]string) map[string][]variables.Variable {
	values := make(map[string][]variables.Variable, len(cfg.Apps))
	for _, a := range cfg.Apps {
		values[a.Name] = nil
	}
	appurl.Prepend(cfg, values, urls)
	return values
}
