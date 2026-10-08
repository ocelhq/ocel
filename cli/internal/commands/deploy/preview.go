package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/valuestore"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/edge"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type previewUpOptions struct {
	name       string
	persistent bool
	prebuilt   bool
	yes        bool
	dry        bool
}

type previewRemoveOptions struct {
	name string
	yes  bool
}

type previewPruneOptions struct {
	name string
	keep int
	yes  bool
}

const defaultPreviewPruneKeepN = 3

func NewPreviewCommand(dependencies Dependencies) *cobra.Command {
	var upOpts previewUpOptions

	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Deploy a preview of the current branch",
		Long: "Deploy a preview of the current branch.\n\n" +
			"`ocel preview` on its own is `ocel preview up`: it deploys the current branch's preview. " +
			"To address a preview by name, pass the name to a subcommand, as in `ocel preview up <name>`.\n\n" +
			"A preview is a full deployment beside production, torn down without touching anything else.",
		Example: "  $ ocel preview\n" +
			"  $ ocel preview up pr-12\n" +
			"  $ ocel preview up staging --persistent\n" +
			"  $ ocel preview ls\n" +
			"  $ ocel preview rm pr-12",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("`ocel preview` takes no name; to deploy the preview %q, run `ocel preview up %s`", args[0], args[0])
			}
			return nil
		},
		RunE: previewUpRunE(dependencies, &upOpts),
	}
	previewUpFlags(cmd, &upOpts)

	up := &cobra.Command{
		Use:   "up [name]",
		Short: "Deploy or refresh a preview",
		Long: "Deploy or refresh a preview.\n\n" +
			"Without a name the preview is the current branch's: deploying the same branch again replaces it, " +
			"and `ocel preview rm` tears it down. A name addresses one preview from any branch. " +
			"A name is a DNS label with no `--`.\n\n" +
			"A preview is ephemeral: it gets no infrastructure of its own, only what bindings share, " +
			"and is torn down without a question. --persistent deploys one with its own infrastructure that " +
			"asks before it is torn down — a staging environment. A preview keeps the lifecycle it was created with.\n\n" +
			"--dry builds, then prints every change the preview would make to your account and stops.",
		Example: "  $ ocel preview up\n" +
			"  $ ocel preview up pr-12\n" +
			"  $ ocel preview up staging --persistent\n" +
			"  $ ocel preview up --dry",
		Args: cobra.MaximumNArgs(1),
		RunE: previewUpRunE(dependencies, &upOpts),
	}
	previewUpFlags(up, &upOpts)

	var rmOpts previewRemoveOptions
	rm := &cobra.Command{
		Use:   "rm [name]",
		Short: "Tear down a preview",
		Long: "Tear down a preview.\n\n" +
			"Without a name it takes down the current branch's preview. A persistent preview asks for confirmation first; " +
			"--yes skips that, and without a terminal to ask on it is required.",
		Example: "  $ ocel preview rm\n" +
			"  $ ocel preview rm pr-12\n" +
			"  $ ocel preview rm staging --yes",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			opts := rmOpts
			opts.name = readPreviewNameArgument(args)
			return runPreviewRemove(cmd.Context(), dependencies, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}
	commands.AddYesFlag(rm, &rmOpts.yes)

	ls := &cobra.Command{
		Use:     "ls",
		Short:   "List this project's previews",
		Example: "  $ ocel preview ls",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return runPreviewList(cmd.Context(), dependencies, cwd, cmd.OutOrStdout())
		},
	}

	var pruneOpts previewPruneOptions
	prune := &cobra.Command{
		Use:   "prune [name]",
		Short: "Delete a preview's old deployments",
		Long: "Delete a preview's old deployments.\n\n" +
			"Keeps the newest --keep deployments and whatever is live. Without a name it prunes the " +
			"current branch's preview.",
		Example: "  $ ocel preview prune\n" +
			"  $ ocel preview prune staging --keep 5",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			opts := pruneOpts
			opts.name = readPreviewNameArgument(args)
			return runPreviewPrune(cmd.Context(), dependencies, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}
	prune.Flags().IntVar(&pruneOpts.keep, "keep", defaultPreviewPruneKeepN, "How many recent deployments to keep (the live one always stays)")
	commands.AddYesFlag(prune, &pruneOpts.yes)

	cmd.AddCommand(
		commands.DeclareRunEvents(commands.DeclareMutating(up)),
		commands.DeclareRunEvents(commands.DeclareMutating(rm)),
		commands.DeclareResult(commands.DeclareReadOnly(commands.ReserveStdout(ls)), &resultv1.PreviewListResult{}),
		commands.DeclareRunEvents(commands.DeclareMutating(prune)),
	)
	return commands.DeclareRunEvents(commands.DeclareMutating(cmd))
}

func previewUpFlags(cmd *cobra.Command, opts *previewUpOptions) {
	cmd.Flags().BoolVar(&opts.persistent, "persistent", false, "Deploy a persistent preview: its own infrastructure, and a confirmation before it is torn down")
	cmd.Flags().BoolVar(&opts.prebuilt, "prebuilt", false, prebuiltFlagUsage)
	commands.AddDryFlag(cmd, &opts.dry, dryFlagUsage)
	commands.AddYesFlag(cmd, &opts.yes)
}

func previewUpRunE(dependencies Dependencies, upOpts *previewUpOptions) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		opts := *upOpts
		opts.name = readPreviewNameArgument(args)
		return runPreviewUp(cmd.Context(), dependencies, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	}
}

func runPreviewUp(ctx context.Context, dependencies Dependencies, cwd string, opts previewUpOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	policy, cfg, err := ensureProject(ctx, dependencies, "ocel preview up", cwd, opts.yes, opts.dry, stdout, stdin)
	if err != nil || cfg == nil {
		return err
	}

	env, err := resolveUpEnvironment(dependencies, cwd, opts)
	if err != nil {
		return err
	}

	if !opts.dry {
		if err := deployreport.Clear(cfg.Dir); err != nil {
			return err
		}
	}

	deployTelemetry := watchDeploy(dependencies.Events, cfg, telemetry.DeployTargetPreview, opts.dry)
	var attempt *deployreport.Attempt
	var apps []*consolev1.App
	var succeeded *consolev1.Deployment
	err = dependencies.WithProvider(ctx, cfg, "ocel preview up", previewOpenOptions(policy, cfg), func(ctx context.Context, p commands.ProviderRun) (err error) {
		run, check, provider, read := p.Run, p.Check, p.Provider, p.Preflight
		cfg := p.Project
		facts, err := preflightPreviewUp(ctx, dependencies, policy, check, provider, cfg, read, opts.prebuilt, env)
		check.End(err)
		if err != nil {
			return err
		}
		if facts.declined {
			run.Succeed("Nothing deployed to preview " + env.GetIdentity())
			return nil
		}
		deployed := false
		defer func() {
			if !deployed {
				err = errors.Join(err, forgetUnclaimedPreviewAlias(ctx, provider, cfg.Slug, env, facts.mintedAlias))
			}
		}()
		cfg = facts.project
		if !opts.dry {
			attempt = p.NewAttempt(ctx, consolev1.DeploymentKind_DEPLOYMENT_KIND_PREVIEW_UP, cfg, env, dependencies.DiscoverPRNumber())
		}

		browser := dependencies.IsBrowserReachable(stdin)
		scope := variablescope.Of(cfg, environmentv1.Tier_TIER_PREVIEW, env.GetIdentity())
		scope.Browser = browser
		recovery := variablesRecovery{
			dependencies: dependencies,
			cfg:          cfg,
			provider:     provider,
			tier:         environmentv1.Tier_TIER_PREVIEW,
			newDeclarations: func(synced variables.EnvSource) *variables.Declarations {
				scope := scope
				scope.EnvSource = synced
				return variables.NewDeclarations(valuestore.Store{
					Provider: provider,
					Project:  cfg,
					Tier:     environmentv1.Tier_TIER_PREVIEW,
				}, scope)
			},
			command:        "ocel preview up",
			containerArchs: facts.containerArchs,
			workerCeilings: facts.workerCeilings,
			host:           build.ReadHost(provider.Facts()),
			urls:           facts.urls,
			infra:          newInfraProvisioning(provider, env, facts, opts.dry, opts.prebuilt),
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
			Edge:        cfg.EdgeSelection(),
			Dry:         opts.dry,

			ProjectRegistry:  registry,
			InlineBindings:   inline,
			AliasToken:       facts.builtAlias,
			InfraProvisioned: recovery.infra.isProvisioned(),
		}

		if opts.dry {
			return showDeployPlan(ctx, run, provider, req, fmt.Sprintf("Proposed changes to preview %s", env.GetIdentity()), cfg.Slug, "preview "+env.GetIdentity())
		}

		out, err := streamDeploy(ctx, provider, req)
		var unread error
		apps, unread = appsDeployed(cfg, manifest, out.apps, env)
		if err != nil {
			return err
		}
		deployed = true
		deployTelemetry.noteDeployed()

		if succeeded, err = deployreport.WriteSucceeded(attempt, apps, out.promotion(""), unread); err != nil {
			return err
		}
		run.Succeed(fmt.Sprintf("Deployed %s to preview %s", cfg.Slug, env.GetIdentity()))
		return nil
	})
	deployTelemetry.record(dependencies.RecordEvent, err)
	dependencies.Console.ReportAttempt(ctx, attempt, apps, succeeded, err, stderr)
	return err
}

func refuseMissingPreviewDomain(cfg *project.Project, wildcard *contractv1.PreviewWildcard, id *contractv1.Identity, hostnameRequired bool, check *run.Span) error {
	declared := cfg.Domains.Preview
	base := wildcard.GetBaseDomain()
	configName := filepath.Base(cfg.Path)

	switch {
	case declared == "" && base == "" && !hostnameRequired:
		check.Say("Serving previews on the address each app's release is given")

	case declared == "" && base == "":
		return fmt.Errorf("this project declares no preview domain and this bootstrap has no global one, so a preview deploy has nowhere to serve: "+
			"add a project-level domains.preview wildcard (e.g. `domains: { preview: \"*.preview.acme.com\" }`) to %s, "+
			"or run `ocel domain use '*.preview.acme.com' --preview` once to serve every project's previews on one shared wildcard — "+
			"a preview domain binds to the whole project, which serves every app and every preview under that one wildcard, so it is never declared per app",
			configName)

	case declared == "":
		if err := checkGlobalPreviewDomain(wildcard, id, configName); err != nil {
			return err
		}
		check.Say(fmt.Sprintf("Serving previews on global *.%s", base))

	case declared == edge.PreviewWildcard(base):
		check.Say(fmt.Sprintf("Serving previews on project-level %s, also the global preview domain", declared))

	case base != "":
		check.Say(fmt.Sprintf("Serving previews on project-level %s; global *.%s ignored", declared, base))
	}
	return nil
}

func previewAppNames(cfg *project.Project) []string {
	names := make([]string, 0, len(cfg.Apps))
	for _, app := range cfg.Apps {
		names = append(names, app.Name)
	}
	return names
}

func checkGlobalPreviewDomain(wildcard *contractv1.PreviewWildcard, id *contractv1.Identity, configName string) error {
	base := wildcard.GetBaseDomain()
	if want, have := wildcard.GetEdgeScope(), id.GetEdgeScope(); want != "" && have != "" && want != have {
		return fmt.Errorf("the global preview domain *.%s lives in edge account %s, but this deploy is authenticated to account %s: "+
			"the wildcard can only be served from the account that owns it — "+
			"re-scope this run's edge credentials to %s, or declare this project's own domains.preview in %s",
			base, want, have, want, configName)
	}
	if !wildcard.GetRouteInstalled() {
		return fmt.Errorf("the global preview domain *.%s is recorded, but its wildcard route is not installed, so nothing would answer a preview hostname: "+
			"run `ocel domain use '*.%s' --preview` to reinstall the edge's preview routing and reclaim the wildcard",
			base, base)
	}
	return nil
}

func runPreviewRemove(ctx context.Context, dependencies Dependencies, cwd string, opts previewRemoveOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	env, err := resolvePreviewEnvironment(dependencies, cwd, opts.name, environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED)
	if err != nil {
		return err
	}

	policy := consent.NewPolicy("ocel preview rm", opts.yes, dependencies.CanAsk(stdin), stdout, stdin)

	var removed *consolev1.EnvironmentEvent
	err = dependencies.WithProvider(ctx, cfg, "ocel preview rm", commands.OpenOptions{Tier: environmentv1.Tier_TIER_PREVIEW, Require: readiness.Features}, func(ctx context.Context, p commands.ProviderRun) error {
		run, check, provider := p.Run, p.Check, p.Provider
		recorded, err := readPreview(ctx, provider, cfg.Slug, env)
		if err != nil {
			return err
		}
		if question := composeTeardownQuestion(recorded); question != "" {
			if err := policy.RefuseQuestion(question, "before it tears the preview down"); err != nil {
				return err
			}
			proceed, err := policy.Confirm(ctx, check, consent.Guard{
				ID:       consent.GuardPersistentPreviewRemoval,
				Question: question,
				Action:   fmt.Sprintf("removing the persistent preview %q", env.GetIdentity()),
			})
			if err != nil {
				return err
			}
			if !proceed {
				run.Succeed(fmt.Sprintf("Nothing torn down: preview %s stays", env.GetIdentity()))
				return nil
			}
		}
		env.Lifecycle = recorded.GetLifecycle()

		check.End(nil)

		req := &contractv1.RemoveEnvironmentRequest{
			Environment: env,
			Slug:        cfg.Slug,
			Edge:        cfg.EdgeSelection(),
		}
		if _, err := providerprocess.Stream(ctx, provider, "RemoveEnvironment", req, contractv1connect.ProviderServiceClient.RemoveEnvironment); err != nil {
			return err
		}
		removed = deployreport.NewEnvironmentEvent(consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_PREVIEW_REMOVED, env, run.TraceID(), time.Now(), cfg.Dir, os.Getenv)
		run.Succeed(fmt.Sprintf("Tore down preview %s of %s", env.GetIdentity(), cfg.Slug))
		return nil
	})
	if removed != nil {
		dependencies.Console.ReportEnvironmentEvent(ctx, cfg.Dir, removed, stderr)
	}
	return err
}

func readPreview(ctx context.Context, provider *providerprocess.Provider, slug string, env *environmentv1.Environment) (recorded *contractv1.PreviewEnvironment, err error) {
	err = provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
		got, err := client.GetEnvironment(ctx, &contractv1.GetEnvironmentRequest{Slug: slug, Environment: env})
		recorded = got.GetEnvironment()
		return err
	})
	return recorded, err
}

func composeTeardownQuestion(recorded *contractv1.PreviewEnvironment) string {
	if recorded.GetLifecycle() != environmentv1.Lifecycle_LIFECYCLE_PERSISTENT {
		return ""
	}
	return fmt.Sprintf("Tear down the persistent preview %q?", recorded.GetIdentity())
}

func runPreviewList(ctx context.Context, dependencies Dependencies, cwd string, stdout io.Writer) error {
	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	previews, err := listPreviews(ctx, dependencies, cfg)
	if err != nil {
		return err
	}
	if dependencies.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, previewListResult(previews))
	}
	renderEnvironments(stdout, previews)
	return nil
}

func previewListResult(previews []*contractv1.PreviewEnvironment) *resultv1.PreviewListResult {
	result := &resultv1.PreviewListResult{Previews: make([]*resultv1.PreviewSummary, 0, len(previews))}
	for _, preview := range previews {
		result.Previews = append(result.Previews, &resultv1.PreviewSummary{
			Identity:  preview.GetIdentity(),
			Lifecycle: preview.GetLifecycle(),
			Label:     preview.GetLabel(),
			CreatedAt: terminal.EpochRFC3339(preview.GetCreatedAt()),
			AliasUrls: preview.GetAliasUrls(),
		})
	}
	return result
}

func listPreviews(ctx context.Context, dependencies Dependencies, cfg *project.Project) (previews []*contractv1.PreviewEnvironment, err error) {
	err = dependencies.WithProvider(ctx, cfg, "ocel preview ls", commands.OpenOptions{}, func(ctx context.Context, p commands.ProviderRun) error {
		p.Check.End(nil)
		return p.Provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
			listed, err := client.ListEnvironments(ctx, &contractv1.ListEnvironmentsRequest{Slug: cfg.Slug})
			previews = listed.GetEnvironments()
			return err
		})
	})
	return previews, err
}

func runPreviewPrune(ctx context.Context, dependencies Dependencies, cwd string, opts previewPruneOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	env, err := resolvePreviewEnvironment(dependencies, cwd, opts.name, environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED)
	if err != nil {
		return err
	}

	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	return dependencies.WithProvider(ctx, cfg, "ocel preview prune", commands.OpenOptions{Tier: environmentv1.Tier_TIER_PREVIEW, Require: readiness.Features}, func(ctx context.Context, p commands.ProviderRun) error {
		p.Check.End(nil)
		run, provider := p.Run, p.Provider
		req := &contractv1.RemoveStalePromotionsRequest{
			Slug:        cfg.Slug,
			KeepN:       int32(opts.keep),
			Environment: env,
			Edge:        cfg.EdgeSelection(),
		}
		if _, err := providerprocess.Stream(ctx, provider, "RemoveStalePromotions", req, contractv1connect.ProviderServiceClient.RemoveStalePromotions); err != nil {
			return err
		}
		run.Succeed(fmt.Sprintf("Pruned the promotions of preview %s down to the newest %d", env.GetIdentity(), opts.keep))
		return nil
	})
}

func resolveUpEnvironment(dependencies Dependencies, cwd string, opts previewUpOptions) (*environmentv1.Environment, error) {
	lifecycle := environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL
	if opts.persistent {
		lifecycle = environmentv1.Lifecycle_LIFECYCLE_PERSISTENT
	}
	return resolvePreviewEnvironment(dependencies, cwd, opts.name, lifecycle)
}

func resolvePreviewEnvironment(dependencies Dependencies, cwd, name string, lifecycle environmentv1.Lifecycle) (*environmentv1.Environment, error) {
	if name != "" {
		if err := previewid.ValidateLabel(name); err != nil {
			return nil, err
		}
	}
	return commands.ResolvePreviewEnvironment(cwd, name, lifecycle, dependencies.ReadGitBranch, dependencies.DiscoverPRNumber)
}

func readPreviewNameArgument(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func renderEnvironments(stdout io.Writer, envs []*contractv1.PreviewEnvironment) {
	if len(envs) == 0 {
		fmt.Fprintln(stdout, "No previews.")
		return
	}
	for _, e := range envs {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\tcreated %s\n",
			e.GetIdentity(),
			lifecycleTag(e.GetLifecycle()),
			formatValueOrDash(e.GetLabel()),
			formatValueOrDash(strings.Join(e.GetAliasUrls(), " ")),
			terminal.EpochDate(e.GetCreatedAt()),
		)
	}
}

func lifecycleTag(l environmentv1.Lifecycle) string {
	switch l {
	case environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL:
		return "ephemeral"
	case environmentv1.Lifecycle_LIFECYCLE_PERSISTENT:
		return "persistent"
	default:
		return "—"
	}
}

func formatValueOrDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}
