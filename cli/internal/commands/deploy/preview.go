package deploy

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/deployrecord"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/valuestore"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/edge"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type previewUpOptions struct {
	ref      string
	name     string
	prebuilt bool
	yes      bool
	dry      bool
}

type previewRemoveOptions struct {
	ref  string
	name string
	yes  bool
}

type previewPruneOptions struct {
	ref  string
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
			"A preview is a full deployment beside production, named after the branch that produced it " +
			"and torn down without touching anything else. `ocel preview` on its own is `ocel preview up`.",
		Example: "  $ ocel preview\n" +
			"  $ ocel preview up --name staging\n" +
			"  $ ocel preview ls\n" +
			"  $ ocel preview rm",
		Args: cobra.NoArgs,
		RunE: previewUpRunE(dependencies, &upOpts),
	}
	previewUpFlags(cmd, &upOpts)

	up := &cobra.Command{
		Use:   "up",
		Short: "Deploy or refresh a preview",
		Long: "Deploy or refresh a preview.\n\n" +
			"With no flags the preview is the current branch's: deploying the same branch again replaces it, " +
			"and `ocel preview rm` tears it down. --name instead deploys a preview that keeps its name " +
			"across branches — a staging environment.\n\n" +
			"--dry builds, then prints every change the preview would make to your account and stops.",
		Example: "  $ ocel preview up\n" +
			"  $ ocel preview up --name staging\n" +
			"  $ ocel preview up --ref feature/checkout\n" +
			"  $ ocel preview up --dry",
		Args: cobra.NoArgs,
		RunE: previewUpRunE(dependencies, &upOpts),
	}
	previewUpFlags(up, &upOpts)

	var rmOpts previewRemoveOptions
	rm := &cobra.Command{
		Use:   "rm",
		Short: "Tear down a preview",
		Long: "Tear down a preview.\n\n" +
			"With no flags it takes down the current branch's preview. A named preview asks for " +
			"confirmation first; --yes skips that.",
		Example: "  $ ocel preview rm\n" +
			"  $ ocel preview rm --name staging\n" +
			"  $ ocel preview rm --ref feature/checkout",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			opts := rmOpts
			return runPreviewRemove(cmd.Context(), dependencies, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}
	rm.Flags().StringVar(&rmOpts.ref, "ref", "", "Tear down the preview for this git `ref` instead of the current branch")
	rm.Flags().StringVar(&rmOpts.name, "name", "", "Tear down the named preview")
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
		Use:   "prune",
		Short: "Delete a preview's old deployments",
		Long: "Delete a preview's old deployments.\n\n" +
			"Keeps the newest --keep deployments and whatever is live. With no flags it prunes the " +
			"current branch's preview; --name prunes a named one.",
		Example: "  $ ocel preview prune\n" +
			"  $ ocel preview prune --name staging\n" +
			"  $ ocel preview prune --ref feature/checkout --keep 5",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			opts := pruneOpts
			return runPreviewPrune(cmd.Context(), dependencies, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}
	prune.Flags().StringVar(&pruneOpts.ref, "ref", "", "Prune the preview for this git `ref` instead of the current branch")
	prune.Flags().StringVar(&pruneOpts.name, "name", "", "Prune the named preview")
	prune.Flags().IntVar(&pruneOpts.keep, "keep", defaultPreviewPruneKeepN, "How many recent deployments to keep (the live one always stays)")
	commands.AddYesFlag(prune, &pruneOpts.yes)

	cmd.AddCommand(up, rm, commands.ReserveStdout(ls), prune)
	return cmd
}

func previewUpFlags(cmd *cobra.Command, opts *previewUpOptions) {
	cmd.Flags().StringVar(&opts.name, "name", "", "Deploy the preview with this `name`, kept across branches, instead of the current branch's")
	cmd.Flags().StringVar(&opts.ref, "ref", "", "Deploy the preview for this git `ref` instead of the current branch")
	cmd.Flags().BoolVar(&opts.prebuilt, "prebuilt", false, prebuiltFlagUsage)
	cmd.Flags().BoolVar(&opts.dry, "dry", false, dryFlagUsage)
	commands.AddYesFlag(cmd, &opts.yes)
}

func previewUpRunE(dependencies Dependencies, upOpts *previewUpOptions) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		opts := *upOpts
		return runPreviewUp(cmd.Context(), dependencies, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	}
}

func runPreviewUp(ctx context.Context, dependencies Dependencies, cwd string, opts previewUpOptions, stdout, stderr io.Writer, stdin io.Reader) (err error) {
	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	env, err := resolveUpEnvironment(dependencies, cwd, opts)
	if err != nil {
		return err
	}

	if !opts.dry {
		if err := deployrecord.Clear(cfg.Dir); err != nil {
			return err
		}
	}

	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}
	policy := consent.NewPolicy("ocel preview up", opts.yes, dependencies.StdinIsTerminal(stdin), stdout, stdin)
	policy.DryRun = opts.dry
	if err := policy.Refuse(); err != nil {
		return err
	}

	ctx, run, err := dependencies.Events.Begin(ctx, "ocel preview up", cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	provider, read, err := dependencies.OpenProvider(ctx, check, cfg, previewOpenOptions(opts.dry, cfg))
	if err != nil {
		check.End(err)
		return err
	}
	defer provider.Close()

	facts, err := preflightPreviewUp(ctx, dependencies, policy, check, provider, cfg, read, opts.prebuilt, env.GetIdentity(), stdout, stdin)
	check.End(err)
	if err != nil {
		return err
	}
	if facts.declined {
		run.Succeed("Nothing deployed to preview " + env.GetIdentity())
		return nil
	}
	cfg = facts.project

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
		urls:           facts.urls,
		dry:            opts.dry,
		enabled:        !opts.dry && browser,
	}
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	manifest, inline, err := recovery.buildManifest(ctx, build, opts.prebuilt)
	build.End(err)
	if err != nil {
		return err
	}
	if manifest == nil {
		run.Succeed(nothingToDeploy(cfg))
		return nil
	}

	registry, err := projectRegistry(cfg)
	if err != nil {
		return err
	}

	req := &contractv1.DeployRequest{
		Manifest:    manifest,
		Environment: env,
		Edge:        cfg.EdgeSelection(),
		Dry:         opts.dry,

		ProjectRegistry: registry,
		InlineBindings:  inline,
	}

	if opts.dry {
		return showDeployPlan(ctx, run, provider, req, fmt.Sprintf("Proposed changes to preview %s", env.GetIdentity()), cfg.Slug, "preview "+env.GetIdentity())
	}

	out, err := streamDeploy(ctx, provider, req)
	if err != nil {
		return err
	}

	record, err := deployrecord.New(cfg, manifest, env, "", out.promotionID, out.apps)
	if err != nil {
		return err
	}
	if err := deployrecord.Write(cfg.Dir, record); err != nil {
		return err
	}
	run.Succeed(fmt.Sprintf("Deployed %s to preview %s", cfg.Slug, env.GetIdentity()))
	return nil
}

func requirePreviewDomain(cfg *project.Project, wildcard *contractv1.PreviewWildcard, id *contractv1.Identity, pointer string, check *run.Span) (edge.PreviewSite, error) {
	declared := cfg.Domains.Preview
	base := wildcard.GetBaseDomain()
	configName := filepath.Base(cfg.Path)

	switch {
	case declared == "" && base == "":
		return edge.PreviewSite{}, fmt.Errorf("this project declares no preview domain and this bootstrap has no global one, so a preview deploy has nowhere to serve: "+
			"add a project-level domains.preview wildcard (e.g. `domains: { preview: \"*.preview.acme.com\" }`) to %s, "+
			"or run `ocel domain use '*.preview.acme.com' --preview` once to serve every project's previews on one shared wildcard — "+
			"a preview domain binds to the whole project, which serves every app and every preview under that one wildcard, so it is never declared per app",
			configName)

	case declared == "":
		if err := checkGlobalPreviewDomain(wildcard, id, configName); err != nil {
			return edge.PreviewSite{}, err
		}
		check.Say(fmt.Sprintf("Serving previews on global *.%s", base))

	case declared == edge.PreviewWildcard(base):
		check.Say(fmt.Sprintf("Serving previews on project-level %s, also the global preview domain", declared))

	case base != "":
		check.Say(fmt.Sprintf("Serving previews on project-level %s; global *.%s ignored", declared, base))
	}

	site := edge.ProjectPreview(strings.TrimPrefix(declared, "*."))
	if declared == "" {
		site = edge.SharedPreview(cfg.Slug, base)
	}
	if err := site.LabelProblem(site.Hosts(pointer, previewAppNames(cfg))); err != nil {
		return edge.PreviewSite{}, err
	}
	return site, nil
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
	if g := edge.PreviewGrammarMax; g < wildcard.GetGrammarMin() || g > wildcard.GetGrammarMax() {
		return fmt.Errorf("this CLI names preview hostnames with grammar %d, but the edge serving *.%s speaks %d–%d, so it would not route what this deploy creates: "+
			"run `ocel domain use '*.%s' --preview` to upgrade the edge, or upgrade the CLI if it is the older half",
			g, base, wildcard.GetGrammarMin(), wildcard.GetGrammarMax(), base)
	}
	return nil
}

func runPreviewRemove(ctx context.Context, dependencies Dependencies, cwd string, opts previewRemoveOptions, stdout, stderr io.Writer, stdin io.Reader) (err error) {
	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	env, err := resolvePreviewEnvironment(dependencies, cwd, opts.name, opts.ref)
	if err != nil {
		return err
	}

	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}
	policy := consent.NewPolicy("ocel preview rm", opts.yes, dependencies.StdinIsTerminal(stdin), stdout, stdin)

	ctx, run, err := dependencies.Events.Begin(ctx, "ocel preview rm", cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	provider, _, err := dependencies.OpenProvider(ctx, check, cfg, commands.OpenOptions{Tier: environmentv1.Tier_TIER_PREVIEW, Require: readiness.Features})
	if err != nil {
		check.End(err)
		return err
	}
	defer provider.Close()

	if env.GetLifecycle() == environmentv1.Lifecycle_LIFECYCLE_PERSISTENT {
		proceed, err := policy.Confirm(ctx, check, fmt.Sprintf("Tear down the named preview %q?", env.GetIdentity()))
		if err != nil {
			return err
		}
		if !proceed {
			run.Succeed(fmt.Sprintf("Nothing torn down: preview %s stays", env.GetIdentity()))
			return nil
		}
	}

	check.End(nil)

	req := &contractv1.RemoveEnvironmentRequest{
		Environment: env,
		Slug:        cfg.Slug,
		Edge:        cfg.EdgeSelection(),
	}
	if _, err := providerprocess.Stream(ctx, provider, "RemoveEnvironment", req, contractv1connect.ProviderServiceClient.RemoveEnvironment); err != nil {
		return err
	}
	run.Succeed(fmt.Sprintf("Tore down preview %s of %s", env.GetIdentity(), cfg.Slug))
	return nil
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
	renderEnvironments(stdout, previews)
	return nil
}

func listPreviews(ctx context.Context, dependencies Dependencies, cfg *project.Project) (previews []*contractv1.PreviewEnvironment, err error) {
	if _, err := cfg.RequireProvider(); err != nil {
		return nil, err
	}

	ctx, run, err := dependencies.Events.Begin(ctx, "ocel preview ls", cfg.Dir)
	if err != nil {
		return nil, err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	provider, _, err := dependencies.OpenProvider(ctx, check, cfg, commands.OpenOptions{})
	check.End(err)
	if err != nil {
		return nil, err
	}
	defer provider.Close()

	var listed *contractv1.ListEnvironmentsResponse
	err = provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		listed, err = client.ListEnvironments(ctx, &contractv1.ListEnvironmentsRequest{Slug: cfg.Slug})
		return err
	})
	return listed.GetEnvironments(), err
}

func runPreviewPrune(ctx context.Context, dependencies Dependencies, cwd string, opts previewPruneOptions, stdout, stderr io.Writer, stdin io.Reader) (err error) {
	env, err := resolvePreviewEnvironment(dependencies, cwd, opts.name, opts.ref)
	if err != nil {
		return err
	}

	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}

	ctx, run, err := dependencies.Events.Begin(ctx, "ocel preview prune", cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	provider, _, err := dependencies.OpenProvider(ctx, check, cfg, commands.OpenOptions{Tier: environmentv1.Tier_TIER_PREVIEW, Require: readiness.Features})
	check.End(err)
	if err != nil {
		return err
	}
	defer provider.Close()

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
}

func persistentPreviewEnvironment(name string) (*environmentv1.Environment, error) {
	if err := previewid.ValidateLabel(name); err != nil {
		return nil, err
	}
	return &environmentv1.Environment{
		Tier:      environmentv1.Tier_TIER_PREVIEW,
		Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
		Identity:  name,
	}, nil
}

func resolveUpEnvironment(dependencies Dependencies, cwd string, opts previewUpOptions) (*environmentv1.Environment, error) {
	if opts.name != "" && opts.ref != "" {
		return nil, fmt.Errorf("pass --name or --ref, not both: a preview is either named or a branch's")
	}
	if opts.name != "" {
		return persistentPreviewEnvironment(opts.name)
	}

	ref, prNumber := opts.ref, ""
	if ref == "" {
		branch, err := dependencies.ReadGitBranch(cwd)
		if err != nil {
			return nil, err
		}
		ref, prNumber = branch, dependencies.DiscoverPRNumber()
	}
	id, err := previewid.Resolve(ref, prNumber)
	if err != nil {
		return nil, err
	}
	return &environmentv1.Environment{
		Tier:      environmentv1.Tier_TIER_PREVIEW,
		Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
		Identity:  id.Key,
		Label:     id.Label,
	}, nil
}

func resolvePreviewEnvironment(dependencies Dependencies, cwd, name, ref string) (*environmentv1.Environment, error) {
	if name != "" && ref != "" {
		return nil, fmt.Errorf("pass --name or --ref, not both: a preview is either named or a branch's")
	}
	if name != "" {
		return persistentPreviewEnvironment(name)
	}

	if ref == "" {
		branch, err := dependencies.ReadGitBranch(cwd)
		if err != nil {
			return nil, err
		}
		ref = branch
	}
	id, err := previewid.Resolve(ref, "")
	if err != nil {
		return nil, err
	}
	return &environmentv1.Environment{
		Tier:      environmentv1.Tier_TIER_PREVIEW,
		Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
		Identity:  id.Key,
		Label:     id.Label,
	}, nil
}

func renderEnvironments(stdout io.Writer, envs []*contractv1.PreviewEnvironment) {
	if len(envs) == 0 {
		fmt.Fprintln(stdout, "No previews.")
		return
	}
	for _, e := range envs {
		fmt.Fprintf(stdout, "%s\t%s\t%s\tcreated %s\n",
			e.GetIdentity(),
			lifecycleTag(e.GetLifecycle()),
			labelOrDash(e.GetLabel()),
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
		return "unknown"
	}
}

func labelOrDash(label string) string {
	if label == "" {
		return "—"
	}
	return label
}
