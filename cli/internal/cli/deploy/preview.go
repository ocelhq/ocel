package deploy

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/preflight"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/deployresult"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/cli/internal/servicemap"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type previewUpOptions struct {
	ref      string
	name     string
	prebuilt bool
	yes      bool
	dry      bool
}

type previewRmOptions struct {
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

func NewPreviewCommand(deps cmddeps.Deps) *cobra.Command {
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
		RunE: previewUpRunE(deps, &upOpts),
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
		RunE: previewUpRunE(deps, &upOpts),
	}
	previewUpFlags(up, &upOpts)

	var rmOpts previewRmOptions
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
			return runPreviewRm(cmd.Context(), deps, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}
	rm.Flags().StringVar(&rmOpts.ref, "ref", "", "Tear down the preview for this git `ref` instead of the current branch")
	rm.Flags().StringVar(&rmOpts.name, "name", "", "Tear down the named preview")
	cmddeps.Yes(rm, &rmOpts.yes)

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
			return runPreviewLs(cmd.Context(), deps, cwd, cmd.OutOrStdout())
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
			return runPreviewPrune(cmd.Context(), deps, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}
	prune.Flags().StringVar(&pruneOpts.ref, "ref", "", "Prune the preview for this git `ref` instead of the current branch")
	prune.Flags().StringVar(&pruneOpts.name, "name", "", "Prune the named preview")
	prune.Flags().IntVar(&pruneOpts.keep, "keep", defaultPreviewPruneKeepN, "How many recent deployments to keep (the live one always stays)")
	cmddeps.Yes(prune, &pruneOpts.yes)

	cmd.AddCommand(up, rm, cmddeps.ReserveStdout(ls), prune)
	return cmd
}

func previewUpFlags(cmd *cobra.Command, opts *previewUpOptions) {
	cmd.Flags().StringVar(&opts.name, "name", "", "Deploy the preview with this `name`, kept across branches, instead of the current branch's")
	cmd.Flags().StringVar(&opts.ref, "ref", "", "Deploy the preview for this git `ref` instead of the current branch")
	cmd.Flags().BoolVar(&opts.prebuilt, "prebuilt", false, prebuiltFlagUsage)
	cmd.Flags().BoolVar(&opts.dry, "dry", false, dryFlagUsage)
	cmddeps.Yes(cmd, &opts.yes)
}

func previewUpRunE(deps cmddeps.Deps, upOpts *previewUpOptions) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		opts := *upOpts
		return runPreviewUp(cmd.Context(), deps, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	}
}

func runPreviewUp(ctx context.Context, deps cmddeps.Deps, cwd string, opts previewUpOptions, stdout, stderr io.Writer, stdin io.Reader) (err error) {
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}

	env, err := resolveUpEnvironment(deps, cwd, opts)
	if err != nil {
		return err
	}

	if !opts.dry {
		if err := deployresult.Clear(cfg.Dir); err != nil {
			return err
		}
		if err := servicemap.Clear(cfg.Dir); err != nil {
			return err
		}
	}

	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}
	gate := deps.Gate(consent.Convergent, "ocel preview up", opts.yes, stdout, stdin)
	gate.Dry = opts.dry
	if err := gate.Refuse(); err != nil {
		return err
	}

	ctx, run, err := deps.Events.Begin(ctx, "ocel preview up", cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := providerclient.Start(ctx, cfg, check, deps.HostTrust, providerclient.ChoosePinning(opts.dry))
	if err != nil {
		return err
	}
	defer prov.Close()

	facts, err := preflightPreviewUp(ctx, deps, gate, check, prov, cfg, env.GetIdentity(), stdout, stdin)
	check.End(err)
	if err != nil {
		return err
	}
	if facts.declined {
		run.Finish("Nothing deployed to preview " + env.GetIdentity())
		return nil
	}

	browser := deps.BrowserReachable(stdin)
	scope := envwire.Scope(cfg, true, env.GetIdentity())
	scope.Browser = browser
	recovery := gateRecovery{
		deps:    deps,
		cfg:     cfg,
		prov:    prov,
		preview: true,
		newGate: func(synced envgate.EnvSource) *envgate.Gate {
			scope := scope
			scope.EnvSource = synced
			return envgate.New(envwire.Values{
				Provider: prov,
				Slug:     cfg.Slug,
				Tier:     environmentv1.Tier_TIER_PREVIEW,
			}, scope)
		},
		command:        "ocel preview up",
		compute:        facts.compute,
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
		run.Finish(nothingToDeploy(cfg))
		return nil
	}

	registry, err := imageRegistry(ctx, prov, cfg, env.GetTier())
	if err != nil {
		return err
	}

	req := &contractv1.DeployRequest{
		Manifest:    manifest,
		Environment: env,
		Edge:        edgewire.Selection(cfg),
		Dry:         opts.dry,

		ImageRegistry: registry,
	}

	if opts.dry {
		return showDeployPlan(ctx, run, prov, req, fmt.Sprintf("Proposed changes to preview %s", env.GetIdentity()), cfg.Slug, "preview "+env.GetIdentity())
	}

	out, err := streamDeploy(ctx, prov, cfg.Slug, req, inline)
	if err != nil {
		return err
	}

	if err := recordDeployResult(cfg, manifest, env, "", out.promotionID, out.apps); err != nil {
		return err
	}
	if err := publishServiceMap(cfg, manifest, env, "", out.promotionID, out.bindings); err != nil {
		return err
	}
	run.Deployed(fmt.Sprintf("Deployed %s to preview %s", cfg.Slug, env.GetIdentity()), out.urlNotes, out.flip)
	return nil
}

func requirePreviewDomain(cfg *projectconfig.Config, wildcard *contractv1.PreviewWildcard, id *contractv1.Identity, pointer string, check *events.Scope) (edge.PreviewSite, error) {
	declared := ""
	if hosts := preflight.Hostnames(cfg, "preview"); len(hosts) > 0 {
		declared = hosts[0].Name
	}
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

func previewAppNames(cfg *projectconfig.Config) []string {
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
			"run `ocel domain use '*.%s' --preview` to reinstall the shared entry worker and reclaim the wildcard",
			base, base)
	}
	if g := edge.PreviewGrammarMax; g < wildcard.GetGrammarMin() || g > wildcard.GetGrammarMax() {
		return fmt.Errorf("this CLI names preview hostnames with grammar %d, but the shared entry worker on *.%s speaks %d–%d, so it would not route what this deploy creates: "+
			"run `ocel domain use '*.%s' --preview` to upgrade the worker, or upgrade the CLI if it is the older half",
			g, base, wildcard.GetGrammarMin(), wildcard.GetGrammarMax(), base)
	}
	return nil
}

func runPreviewRm(ctx context.Context, deps cmddeps.Deps, cwd string, opts previewRmOptions, stdout, stderr io.Writer, stdin io.Reader) (err error) {
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}

	env, err := resolvePreviewEnvironment(deps, cwd, opts.name, opts.ref)
	if err != nil {
		return err
	}

	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}
	gate := deps.Gate(consent.Convergent, "ocel preview rm", opts.yes, stdout, stdin)

	ctx, run, err := deps.Events.Begin(ctx, "ocel preview rm", cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := providerclient.Start(ctx, cfg, check, deps.HostTrust, providerclient.PinToLock)
	if err != nil {
		return err
	}
	defer prov.Close()

	if env.GetLifecycle() == environmentv1.Lifecycle_LIFECYCLE_PERSISTENT {
		proceed, err := gate.Guard(ctx, check, fmt.Sprintf("Tear down the named preview %q?", env.GetIdentity()))
		if err != nil {
			return err
		}
		if !proceed {
			run.Finish(fmt.Sprintf("Nothing torn down: preview %s stays", env.GetIdentity()))
			return nil
		}
	}

	err = preflightPreview(ctx, check, prov, cfg)
	check.End(err)
	if err != nil {
		return err
	}

	req := &contractv1.RemoveEnvironmentRequest{
		Environment: env,
		Slug:        cfg.Slug,
		Edge:        edgewire.Selection(cfg),
	}
	if _, err := providerclient.Stream(ctx, prov, "RemoveEnvironment", req, contractv1connect.ProviderServiceClient.RemoveEnvironment); err != nil {
		return err
	}
	run.Finish(fmt.Sprintf("Tore down preview %s of %s", env.GetIdentity(), cfg.Slug))
	return nil
}

func runPreviewLs(ctx context.Context, deps cmddeps.Deps, cwd string, stdout io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}
	previews, err := listPreviews(ctx, deps, cfg)
	if err != nil {
		return err
	}
	renderEnvironments(stdout, previews)
	return nil
}

func listPreviews(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config) (previews []*contractv1.PreviewEnvironment, err error) {
	if _, err := cfg.RequireProvider(); err != nil {
		return nil, err
	}

	ctx, run, err := deps.Events.Begin(ctx, "ocel preview ls", cfg.Dir)
	if err != nil {
		return nil, err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := providerclient.Start(ctx, cfg, check, deps.HostTrust, providerclient.PinToLock)
	check.End(err)
	if err != nil {
		return nil, err
	}
	defer prov.Close()

	var listed *contractv1.ListEnvironmentsResponse
	err = prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		listed, err = client.ListEnvironments(ctx, &contractv1.ListEnvironmentsRequest{Slug: cfg.Slug})
		return err
	})
	return listed.GetEnvironments(), err
}

func runPreviewPrune(ctx context.Context, deps cmddeps.Deps, cwd string, opts previewPruneOptions, stdout, stderr io.Writer, stdin io.Reader) (err error) {
	env, err := resolvePreviewEnvironment(deps, cwd, opts.name, opts.ref)
	if err != nil {
		return err
	}

	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}

	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}

	ctx, run, err := deps.Events.Begin(ctx, "ocel preview prune", cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := providerclient.Start(ctx, cfg, check, deps.HostTrust, providerclient.PinToLock)
	if err != nil {
		return err
	}
	defer prov.Close()

	err = preflightPreview(ctx, check, prov, cfg)
	check.End(err)
	if err != nil {
		return err
	}

	req := &contractv1.RemoveStalePromotionsRequest{
		Slug:        cfg.Slug,
		KeepN:       int32(opts.keep),
		Environment: env,
		Edge:        edgewire.Selection(cfg),
	}
	if _, err := providerclient.Stream(ctx, prov, "RemoveStalePromotions", req, contractv1connect.ProviderServiceClient.RemoveStalePromotions); err != nil {
		return err
	}
	run.Finish(fmt.Sprintf("Pruned the promotions of preview %s down to the newest %d", env.GetIdentity(), opts.keep))
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

func resolveUpEnvironment(deps cmddeps.Deps, cwd string, opts previewUpOptions) (*environmentv1.Environment, error) {
	if opts.name != "" && opts.ref != "" {
		return nil, fmt.Errorf("pass --name or --ref, not both: a preview is either named or a branch's")
	}
	if opts.name != "" {
		return persistentPreviewEnvironment(opts.name)
	}

	ref, prNumber := opts.ref, ""
	if ref == "" {
		branch, err := deps.CurrentGitBranch(cwd)
		if err != nil {
			return nil, err
		}
		ref, prNumber = branch, deps.DiscoverPRNumber()
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

func resolvePreviewEnvironment(deps cmddeps.Deps, cwd, name, ref string) (*environmentv1.Environment, error) {
	if name != "" && ref != "" {
		return nil, fmt.Errorf("pass --name or --ref, not both: a preview is either named or a branch's")
	}
	if name != "" {
		return persistentPreviewEnvironment(name)
	}

	if ref == "" {
		branch, err := deps.CurrentGitBranch(cwd)
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
			runui.EpochDate(e.GetCreatedAt()),
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
