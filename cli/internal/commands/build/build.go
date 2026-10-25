package build

import (
	"context"
	"errors"
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
	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/progress"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type Dependencies struct {
	commands.Invocation
	BuildApps           func(ctx context.Context, cfg *project.Project, variables map[string]build.AppVariables, archs map[string]string, workers build.HostedWorkers, host build.Host, log build.Log) (build.Output, error)
	CollectDeclarations func(ctx context.Context, cfg *project.Project, declarations *variables.Declarations, stdout, stderr io.Writer) ([]declaration.Resource, error)
	OpenBrowser         func(url string) error
	ReadGitBranch       func(dir string) (string, error)
	DiscoverPRNumber    func() string
}

type options struct {
	noWait bool
	env    string
}

func NewCommand(dependencies Dependencies) *cobra.Command {
	var opts options
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build every app in your project without deploying",
		Long: "Build every app in your project without deploying: a serverless app's functions\n" +
			"into " + buildoutput.Dir + ", and a container app's image into the local docker daemon.\n" +
			"`ocel deploy --prebuilt` deploys what this built.\n\n" +
			"Express, Fastify and Hono servers are bundled, so only what the entrypoint imports\n" +
			"reaches the function: static directories, view templates and files read at run\n" +
			"time are left behind. Set OCEL_BUILD_PREFER_TRACING=1 to copy the dependency\n" +
			"tree instead.\n\n" +
			"Without --env the build reads no tier's values, so code that reads a declared variable\n" +
			"while it builds sees it unset. --env reads the values that tier holds now, as the last\n" +
			"deploy or `ocel env sync` left them, without reading its env source, and hands them to\n" +
			"the build as `ocel deploy` does: plaintext in the build's environment, sensitive and\n" +
			"secret values in its live directory. The output then deploys with --prebuilt only to\n" +
			"that environment.",
		Example: "  $ ocel build\n" +
			"  $ ocel build --env production",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			return runBuild(cmd.Context(), dependencies, cwd, opts, cmd.InOrStdin())
		},
	}
	commands.AddNoWaitFlag(cmd, &opts.noWait)
	cmd.Flags().StringVar(&opts.env, "env", "", "Build with the values of a deployed `environment`: production, or preview for the current branch's preview")
	return commands.DeclareRunEvents(commands.DeclareMutating(cmd))
}

func runBuild(ctx context.Context, dependencies Dependencies, cwd string, opts options, stdin io.Reader) (err error) {
	var env *environmentv1.Environment
	if opts.env != "" {
		if env, err = commands.ResolveDeployedEnvironment(cwd, opts.env, dependencies.ReadGitBranch, dependencies.DiscoverPRNumber); err != nil {
			return err
		}
	}
	resolved, err := dependencies.ResolveTarget(ctx, cwd)
	if err != nil {
		return err
	}
	if resolved.IsCloud() {
		if env != nil {
			return commands.NotOnCloud("ocel build --env")
		}
		job := commands.CloudJob{Command: "ocel build", Tier: environmentv1.Tier_TIER_UNSPECIFIED, Kind: cloudv1.JobCommand_JOB_COMMAND_BUILD, NoWait: opts.noWait}
		if dependencies.IsBrowserReachable(stdin) {
			job.OpenBrowser = dependencies.OpenBrowser
		}
		return dependencies.RunOnCloud(ctx, resolved, job)
	}
	if opts.noWait {
		return commands.RefuseNoWait()
	}
	declared := resolved.Project
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
	if len(declared.Apps) == 0 {
		return nothingToBuild(declared)
	}
	cfg, host, provider, err := resolveBuild(ctx, dependencies, building, declared, env)
	if err != nil {
		return err
	}
	if provider != nil {
		defer provider.Close()
	}
	phase := building.Phase(progressv1.Phase_PHASE_BUILD)

	declarations, err := readDeclarations(ctx, provider, cfg, env)
	if err != nil {
		return err
	}
	workers, err := collectDeclarations(ctx, dependencies, cfg, declarations, phase)
	if err != nil {
		return err
	}
	values, secrets, err := resolveValues(ctx, declarations, cfg, env)
	if err != nil {
		return err
	}
	clients := variables.ClientAppsOf(cfg, declarations.Definitions(), values)
	releaseOutput, err := workdir.LockBuildOutput(ctx, cfg.Dir, func() {
		phase.Say("Waiting for another ocel run in " + cfg.Dir + " to finish with " + buildoutput.Dir)
	})
	if err != nil {
		return err
	}
	defer releaseOutput()
	built, err := dependencies.BuildApps(run.ContextWithSpan(ctx, phase), cfg, build.SplitVariablesByClass(clients, secrets), declaredArchs(cfg), workers, host, appBuildLog(phase))
	if err != nil {
		return err
	}
	if err := variables.RecordClientValues(cfg.Dir, env, clients); err != nil {
		return err
	}
	phase.End(nil)
	for _, line := range builtLines(built) {
		phase.Say(line)
	}
	building.SetApps(builtApps(cfg, built))
	building.Succeed(builtHeadline(built))
	return nil
}

func builtApps(cfg *project.Project, built build.Output) []*progressv1.AppResult {
	apps := make([]*progressv1.AppResult, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		apps = append(apps, &progressv1.AppResult{App: a.Name, Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED, BuildId: built.BuildIDs[a.Name]})
	}
	return apps
}

func nothingToBuild(cfg *project.Project) error {
	cause := errors.New("Nothing to build: " + cfg.Slug + ` declares no apps: name the app to build under "apps"`)
	if cfg.RootAppUndetected != nil {
		cause = errors.New("Nothing to build: " + cfg.Slug + " declares no apps, and " + cfg.RootAppUndetected.Error() + `: name the app to build under "apps"`)
	}
	return &clierror.Error{Code: clierror.CodeProjectInvalidConfig, Hint: "apps", Cause: cause}
}

func readDeclarations(ctx context.Context, provider *providerprocess.Provider, cfg *project.Project, env *environmentv1.Environment) (*variables.Declarations, error) {
	if env == nil {
		return variables.NewDeclarations(variables.NoValues{}, variables.Scope{Apps: variables.AppsOf(cfg)}), nil
	}
	store := variables.Store{Provider: provider, Project: cfg, Tier: env.GetTier()}
	scope := variables.ScopeOf(cfg, env.GetTier(), env.GetIdentity())
	described, err := store.DescribeEnvSource(ctx)
	if err != nil {
		return nil, err
	}
	scope.EnvSource = described
	return variables.NewDeclarations(store, scope), nil
}

func collectDeclarations(ctx context.Context, dependencies Dependencies, cfg *project.Project, declarations *variables.Declarations, phase *run.Span) (build.HostedWorkers, error) {
	said := phase.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED)
	resources, err := dependencies.CollectDeclarations(ctx, cfg, declarations, said, said)
	if err != nil {
		return nil, err
	}
	placement, err := manifest.PlaceConsumers(cfg, resources)
	if err != nil {
		return nil, err
	}
	return placement.HostedWorkers(), nil
}

func resolveValues(ctx context.Context, declarations *variables.Declarations, cfg *project.Project, env *environmentv1.Environment) (map[string][]variables.Variable, map[string]map[string]string, error) {
	if env == nil {
		return builtInValues(cfg, appurl.FormatProductionURLs(cfg)), nil, nil
	}
	if err := declarations.RefuseIncomplete(); err != nil {
		return nil, nil, err
	}
	values, err := declarations.ResolveVariables(ctx, variables.AppsOf(cfg))
	if err != nil {
		return nil, nil, err
	}
	if env.GetTier() == environmentv1.Tier_TIER_PRODUCTION {
		appurl.Prepend(cfg, values, appurl.FormatProductionURLs(cfg))
	}
	secrets, err := build.RevealSecrets(ctx, declarations, cfg, nil)
	if err != nil {
		return nil, nil, err
	}
	return values, secrets, nil
}

func resolveBuild(ctx context.Context, dependencies Dependencies, building *run.Run, declared *project.Project, env *environmentv1.Environment) (resolved *project.Project, host build.Host, provider *providerprocess.Provider, err error) {
	if len(declared.UnresolvedApps()) == 0 {
		resolved, err = declared.ResolveDeclaredComputes()
		if err != nil {
			return nil, build.Host{}, nil, err
		}
		hosted, err := build.FindAppsBuiltForTheirHost(resolved)
		if err != nil || env == nil && len(hosted) == 0 {
			return resolved, build.Host{}, nil, err
		}
	}
	check := building.Phase(progressv1.Phase_PHASE_CHECK)
	defer func() { check.End(err) }()
	provider, _, err = dependencies.OpenProvider(ctx, check, declared, openOptions(env))
	if err != nil {
		return nil, build.Host{}, nil, err
	}
	resolved, err = readiness.ResolveComputes(provider, declared)
	if err != nil {
		provider.Close()
		return nil, build.Host{}, nil, err
	}
	return resolved, build.ReadHost(provider.Facts()), provider, nil
}

func openOptions(env *environmentv1.Environment) commands.OpenOptions {
	if env == nil {
		return commands.OpenOptions{}
	}
	return commands.OpenOptions{Intent: commands.UseInfrastructure, Tier: env.GetTier()}
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
