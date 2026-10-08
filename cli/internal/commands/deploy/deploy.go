package deploy

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/valuestore"
	"github.com/ocelhq/ocel/cli/internal/variableeditor"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
)

const prebuiltFlagUsage = "Deploy the existing " + statedir.Name + "/output instead of building first (produce it with ocel build)"

type deployOptions struct {
	yes      bool
	dry      bool
	tag      string
	prebuilt bool
}

type Dependencies struct {
	commands.Invocation
	BuildApps               func(ctx context.Context, cfg *project.Project, variables map[string]build.AppVariables, archs map[string]string, workers build.HostedWorkers, host build.Host, log build.Log) (build.Output, error)
	RefuseUnbuildableImages func(ctx context.Context, span *run.Span, cfg *project.Project, archs map[string]string) error
	ReadPrebuilt            func(ctx context.Context, cfg *project.Project, archs map[string]string) (build.Output, error)
	BuildID                 func(projectDir, app string) (string, error)
	CollectDeclarations     func(ctx context.Context, cfg *project.Project, declarations *variables.Declarations, stdout, stderr io.Writer) ([]declaration.Resource, error)
	OpenBrowser             func(url string) error
	ServeVariableEditor     func(ctx context.Context, cfg *project.Project, provider *providerprocess.Provider, tier environmentv1.Tier, declarations *variables.Declarations, recovery *variableeditor.Recovery) (*variableeditor.Session, error)
	ReadGitBranch           func(dir string) (string, error)
	DiscoverPRNumber        func() string
}

func NewCommand(dependencies Dependencies) *cobra.Command {
	var opts deployOptions

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy this project to your own infrastructure",
		Long: "Deploy this project to your own infrastructure.\n\n" +
			"Provisions the resources the project declares, builds the apps, and releases the result " +
			"into your provider account. Every deploy is kept: list them with `ocel promotions ls`, " +
			"return to one with `ocel rollback`.\n\n" +
			"--dry builds, then prints every change the deploy would make to your account and stops.",
		Example: "  $ ocel deploy\n" +
			"  $ ocel deploy --tag v1.2.0\n" +
			"  $ ocel deploy --prebuilt\n" +
			"  $ ocel deploy --dry\n" +
			"  $ ocel deploy --yes",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			opts := opts
			return runDeploy(cmd.Context(), dependencies, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}

	commands.AddYesFlag(cmd, &opts.yes)
	cmd.Flags().StringVar(&opts.tag, "tag", "", "Mark this deploy with an immutable `label` to roll back to by name (ocel rollback --tag)")
	cmd.Flags().BoolVar(&opts.prebuilt, "prebuilt", false, prebuiltFlagUsage)
	commands.AddDryFlag(cmd, &opts.dry, dryFlagUsage)

	return commands.DeclareRunEvents(commands.DeclareMutating(cmd))
}

func runDeploy(ctx context.Context, dependencies Dependencies, cwd string, opts deployOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	policy, cfg, err := ensureProject(ctx, dependencies, "ocel deploy", cwd, opts.yes, opts.dry, stdout, stdin)
	if err != nil || cfg == nil {
		return err
	}

	if !opts.dry {
		if err := deployreport.Clear(cfg.Dir); err != nil {
			return err
		}
	}

	deployTelemetry := watchDeploy(dependencies.Events, cfg, telemetry.DeployTargetProduction, opts.dry)
	var attempt *deployreport.Attempt
	var apps []*consolev1.App
	var succeeded *consolev1.Deployment
	err = dependencies.WithProvider(ctx, cfg, "ocel deploy", productionOpenOptions(policy, cfg), func(ctx context.Context, p commands.ProviderRun) error {
		run, check, provider, read := p.Run, p.Check, p.Provider, p.Preflight
		cfg := p.Project
		facts, err := preflightDeploy(ctx, dependencies, policy, check, provider, cfg, read, opts.prebuilt)
		check.End(err)
		if err != nil {
			return err
		}
		if facts.declined {
			run.Succeed("Nothing deployed to production")
			return nil
		}
		cfg = facts.project
		env := &environmentv1.Environment{
			Tier:      environmentv1.Tier_TIER_PRODUCTION,
			Lifecycle: environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED,
		}
		infra := newInfraProvisioning(provider, env, facts, opts.dry, opts.prebuilt)
		defer infra.abandon(ctx, cfg.Slug)
		if !opts.dry {
			attempt = p.NewAttempt(ctx, consolev1.DeploymentKind_DEPLOYMENT_KIND_DEPLOY, cfg, env, dependencies.DiscoverPRNumber())
		}

		browser := dependencies.IsBrowserReachable(stdin)
		scope := variablescope.Of(cfg, environmentv1.Tier_TIER_PRODUCTION, "")
		scope.Browser = browser
		recovery := variablesRecovery{
			dependencies: dependencies,
			cfg:          cfg,
			provider:     provider,
			tier:         environmentv1.Tier_TIER_PRODUCTION,
			newDeclarations: func(synced variables.EnvSource) *variables.Declarations {
				scope := scope
				scope.EnvSource = synced
				return variables.NewDeclarations(valuestore.Store{
					Provider: provider,
					Project:  cfg,
					Tier:     environmentv1.Tier_TIER_PRODUCTION,
				}, scope)
			},
			command:        "ocel deploy",
			containerArchs: facts.containerArchs,
			workerCeilings: facts.workerCeilings,
			host:           build.ReadHost(provider.Facts()),
			urls:           facts.urls,
			infra:          infra,
			preBuild:       findPreBuild(cfg, env),
			dry:            opts.dry,
			enabled:        !opts.dry && browser,
		}
		build := run.Phase(progressv1.Phase_PHASE_BUILD)
		manifest, inline, err := recovery.buildManifest(ctx, build, opts.prebuilt)
		build.End(err)
		if err != nil {
			return err
		}
		deployTelemetry.noteManifest(manifest)
		if manifest == nil {
			run.Succeed(nothingToDeploy(cfg))
			return nil
		}
		apps, _ = appsDeployed(cfg, manifest, nil, env)

		registry, err := readiness.ProjectRegistry(cfg)
		if err != nil {
			return err
		}

		req := &contractv1.DeployRequest{
			Manifest:    manifest,
			Environment: env,
			Tag:         opts.tag,
			Edge:        cfg.EdgeSelection(),
			Dry:         opts.dry,

			ProjectRegistry:  registry,
			InlineBindings:   inline,
			InfraProvisioned: infra.isProvisioned(),
			LeaseToken:       infra.readLeaseToken(),
		}

		if opts.dry {
			return showDeployPlan(ctx, run, provider, req, "Proposed changes to production", cfg.Slug, "production", describePlannedPreBuild(recovery.preBuild, opts.prebuilt))
		}

		out, err := streamDeploy(ctx, provider, req)
		var unread error
		apps, unread = appsDeployed(cfg, manifest, out.apps, env)
		if err != nil {
			return err
		}
		infra.markShipped()
		deployTelemetry.noteDeployed()

		if succeeded, err = deployreport.WriteSucceeded(attempt, apps, out.promotion(opts.tag), unread); err != nil {
			return err
		}
		run.Succeed(fmt.Sprintf("Deployed %s to production", cfg.Slug))
		return nil
	})
	deployTelemetry.record(dependencies.RecordEvent, err)
	dependencies.Console.ReportAttempt(ctx, attempt, apps, succeeded, err, stderr)
	return err
}
