package cli

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

	"github.com/ocelhq/ocel/cli/internal/cli/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/providerui"
	"github.com/ocelhq/ocel/cli/internal/deployresult"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/prompt"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/servicemap"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type previewUpOptions struct {
	ref      string
	name     string
	prebuilt bool
	noUI     bool
}

type previewRmOptions struct {
	ref  string
	name string
	yes  bool
}

type previewPruneOptions struct {
	name string
	keep int
}

var (
	previewUpOpts    previewUpOptions
	previewRmOpts    previewRmOptions
	previewPruneOpts previewPruneOptions
)

const defaultPreviewPruneKeepN = 3

var previewCmd = &cobra.Command{
	Use:   "preview",
	Short: "Stand up a preview environment for the current branch",
	Args:  cobra.NoArgs,
	RunE:  runPreviewUpCmd,
}

var previewUpCmd = &cobra.Command{
	Use:   "up",
	Short: "Stand up (or update) a preview environment",
	Args:  cobra.NoArgs,
	RunE:  runPreviewUpCmd,
}

var previewRmCmd = &cobra.Command{
	Use:   "rm",
	Short: "Tear down a preview environment",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()
		return runPreviewRm(ctx, newDeps(), cwd, previewRmOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

var previewLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List preview environments",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()
		return runPreviewLs(ctx, newDeps(), cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func runPreviewUpCmd(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
	defer stop()
	return runPreviewUp(ctx, newDeps(), cwd, previewUpOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
}

var previewPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Reclaim a named persistent preview's old deployments",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()
		return runPreviewPrune(ctx, newDeps(), cwd, previewPruneOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	previewUpCmd.Flags().StringVar(&previewUpOpts.name, "name", "", "Name a persistent (staging-like) preview instead of the branch's ephemeral one")
	previewCmd.Flags().StringVar(&previewUpOpts.name, "name", "", "Name a persistent (staging-like) preview instead of the branch's ephemeral one")
	previewUpCmd.Flags().StringVar(&previewUpOpts.ref, "ref", "", "Stand up the ephemeral preview for an explicit git ref instead of the current branch")
	previewCmd.Flags().StringVar(&previewUpOpts.ref, "ref", "", "Stand up the ephemeral preview for an explicit git ref instead of the current branch")
	previewUpCmd.Flags().BoolVar(&previewUpOpts.prebuilt, "prebuilt", false, prebuiltFlagUsage)
	previewCmd.Flags().BoolVar(&previewUpOpts.prebuilt, "prebuilt", false, prebuiltFlagUsage)
	previewUpCmd.Flags().BoolVar(&previewUpOpts.noUI, "no-ui", false, noUIFlagUsage)
	previewCmd.Flags().BoolVar(&previewUpOpts.noUI, "no-ui", false, noUIFlagUsage)

	previewRmCmd.Flags().StringVar(&previewRmOpts.ref, "ref", "", "Tear down the ephemeral preview for an explicit git ref")
	previewRmCmd.Flags().StringVar(&previewRmOpts.name, "name", "", "Tear down the named persistent preview")
	previewRmCmd.Flags().BoolVarP(&previewRmOpts.yes, "yes", "y", false, "Skip the confirmation prompt")

	previewPruneCmd.Flags().StringVar(&previewPruneOpts.name, "name", "", "Name of the persistent preview to prune (required — ephemeral previews are not pruned)")
	previewPruneCmd.Flags().IntVar(&previewPruneOpts.keep, "keep", defaultPreviewPruneKeepN, "Number of most recent deployments to keep, always additionally pinning the active one")

	previewCmd.AddCommand(previewUpCmd)
	previewCmd.AddCommand(previewRmCmd)
	previewCmd.AddCommand(previewLsCmd)
	previewCmd.AddCommand(previewPruneCmd)
}

func runPreviewUp(ctx context.Context, deps cmddeps.Deps, cwd string, opts previewUpOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	env, err := resolveUpEnvironment(deps, cwd, opts)
	if err != nil {
		return err
	}

	if err := deployresult.Clear(cfg.Dir); err != nil {
		return err
	}
	if err := servicemap.Clear(cfg.Dir); err != nil {
		return err
	}

	return providerui.Run(ctx, deps, cfg, "ocel preview up", stdout, func(ctx context.Context, runner *provider.Runner, ui *deployui.Session) error {
		if err := preflightPreviewUp(ctx, deps, runner, cfg, env.GetIdentity(), stdout, stdin); err != nil {
			return err
		}

		ui.Building()
		recovery := gateRecovery{
			deps:    deps,
			cfg:     cfg,
			runner:  runner,
			preview: true,
			newGate: func() *envgate.Gate {
				return envgate.New(runnerValues{
					runner: runner,
					slug:   cfg.Slug,
					tier:   environmentv1.Tier_TIER_PREVIEW,
				}, envScope(cfg, true, env.GetIdentity()))
			},
			ui:      ui,
			stdout:  stdout,
			enabled: canOpenVarsUI(deps, stdin, opts.noUI),
		}
		manifest, err := recovery.buildManifest(ctx, opts.prebuilt)
		if err != nil {
			return err
		}
		if manifest == nil {
			ui.Finish("Nothing to deploy")
			return nil
		}
		ui.BuildOK()

		req := &contractv1.DeployRequest{
			Manifest:    manifest,
			Environment: env,
			Edge:        edgewire.Selection(cfg),
		}

		var out deployOutcome
		if err := provider.Stream(ctx, runner, "Deploy", req, contractv1connect.ProviderServiceClient.Deploy, out.collect(ui)); err != nil {
			return err
		}

		if err := recordDeployResult(cfg, manifest, env, "", out.promotionID, out.appURLs); err != nil {
			return err
		}
		if err := publishServiceMap(cfg, manifest, env, "", out.promotionID, out.links); err != nil {
			return err
		}
		ui.Deployed(fmt.Sprintf("Preview %s is up", env.GetIdentity()), out.appURLs, out.urlNote, out.flip, out.links, out.functions)
		return nil
	})
}

func requirePreviewDomain(cfg *projectconfig.Config, wildcard *contractv1.PreviewWildcard, id *contractv1.Identity, pointer string, out io.Writer) error {
	declared := ""
	if hosts := declaredHostnames(cfg, "preview"); len(hosts) > 0 {
		declared = hosts[0]
	}
	base := wildcard.GetBaseDomain()
	configName := filepath.Base(cfg.Path)

	switch {
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
		fmt.Fprintf(out, "Serving previews on the global preview domain *.%s — this project declares no domains.preview of its own\n", base)

	case base != "":
		fmt.Fprintf(out, "Serving previews on %s, this project's own domains.preview — the global preview domain *.%s exists and is ignored (remove domains.preview from %s to serve on it)\n",
			declared, base, configName)
	}

	labelSlug, labelBase := "", strings.TrimPrefix(declared, "*.")
	if declared == "" {
		labelSlug, labelBase = cfg.Slug, base
	}
	return edge.PreviewLabelProblem(labelSlug, intendedPreviewHostnames(cfg, labelSlug, pointer, labelBase))
}

func intendedPreviewHostnames(cfg *projectconfig.Config, slug, pointer, base string) []string {
	if pointer == "" || base == "" {
		return nil
	}
	if len(cfg.Apps) < 2 {
		return []string{edge.PreviewHost(slug, pointer, "", base)}
	}
	hosts := make([]string, 0, len(cfg.Apps))
	for _, app := range cfg.Apps {
		hosts = append(hosts, edge.PreviewHost(slug, pointer, app.Name, base))
	}
	return hosts
}

func checkGlobalPreviewDomain(wildcard *contractv1.PreviewWildcard, id *contractv1.Identity, configName string) error {
	base := wildcard.GetBaseDomain()
	if want, have := wildcard.GetEdgeScope(), id.GetEdgeScope(); want != "" && have != "" && want != have {
		return fmt.Errorf("the global preview domain *.%s lives in edge account %s, but this deploy is authenticated to account %s: "+
			"the wildcard can only be served from the account that holds it — "+
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

func runPreviewRm(ctx context.Context, deps cmddeps.Deps, cwd string, opts previewRmOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	env, err := resolveRmEnvironment(deps, cwd, opts)
	if err != nil {
		return err
	}

	persistent := env.GetLifecycle() == environmentv1.Lifecycle_LIFECYCLE_PERSISTENT
	if persistent && !opts.yes && isReaderTTY(stdin) {
		proceed, err := confirmDestroyPreview(ctx, env.GetIdentity(), stdout, stdin)
		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(stdout, "Interrupted.")
				return &exitsig.ExitError{Code: exitsig.InterruptCode}
			}
			return err
		}
		if !proceed {
			fmt.Fprintln(stdout, "Aborted.")
			return nil
		}
	}

	return providerui.Run(ctx, deps, cfg, "ocel preview rm", stdout, func(ctx context.Context, runner *provider.Runner, ui *deployui.Session) error {
		if err := preflightPreview(ctx, deps, runner, cfg, stdout); err != nil {
			return err
		}

		req := &contractv1.RemoveEnvironmentRequest{
			Environment: env,
			Slug:        cfg.Slug,
			Edge:        edgewire.Selection(cfg),
		}
		if err := provider.Stream(ctx, runner, "RemoveEnvironment", req, contractv1connect.ProviderServiceClient.RemoveEnvironment, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Preview %s torn down", env.GetIdentity()))
		return nil
	})
}

func runPreviewLs(ctx context.Context, deps cmddeps.Deps, cwd string, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	return provider.Drive(ctx, cfg, stdout, stderr, func(runner *provider.Runner) error {
		client, err := runner.Client()
		if err != nil {
			return err
		}
		resp, err := client.ListEnvironments(ctx, &contractv1.ListEnvironmentsRequest{
			Slug: cfg.Slug,
		})
		if err != nil {
			return err
		}
		renderEnvironments(stdout, resp.GetEnvironments())
		return nil
	})
}

func runPreviewPrune(ctx context.Context, deps cmddeps.Deps, cwd string, opts previewPruneOptions, stdout, stderr io.Writer) error {
	if opts.name == "" {
		return fmt.Errorf("`ocel preview prune` requires --name: only persistent previews are pruned")
	}
	env, err := persistentPreviewEnvironment(opts.name)
	if err != nil {
		return err
	}

	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	return providerui.Run(ctx, deps, cfg, "ocel preview prune", stdout, func(ctx context.Context, runner *provider.Runner, ui *deployui.Session) error {
		if err := preflightPreview(ctx, deps, runner, cfg, stdout); err != nil {
			return err
		}
		req := &contractv1.RemoveStalePromotionsRequest{
			Slug:        cfg.Slug,
			KeepN:       int32(opts.keep),
			Environment: env,
			Edge:        edgewire.Selection(cfg),
		}
		if err := provider.Stream(ctx, runner, "RemoveStalePromotions", req, contractv1connect.ProviderServiceClient.RemoveStalePromotions, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Pruned preview %q", opts.name))
		return nil
	})
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
		return nil, fmt.Errorf("--name and --ref are mutually exclusive; use one to stand up a persistent or ephemeral preview")
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
	}, nil
}

func resolveRmEnvironment(deps cmddeps.Deps, cwd string, opts previewRmOptions) (*environmentv1.Environment, error) {
	if opts.name != "" && opts.ref != "" {
		return nil, fmt.Errorf("--name and --ref are mutually exclusive; use one to address a persistent or ephemeral preview")
	}
	if opts.name != "" {
		return persistentPreviewEnvironment(opts.name)
	}

	ref := opts.ref
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
	}, nil
}

func confirmDestroyPreview(ctx context.Context, name string, stdout io.Writer, stdin io.Reader) (bool, error) {
	return prompt.New(stdout, stdin).Confirm(ctx, fmt.Sprintf("Destroy persistent preview %q?", name))
}

func preflightPreview(ctx context.Context, deps cmddeps.Deps, runner *provider.Runner, cfg *projectconfig.Config, out io.Writer) error {
	return preflightTier(ctx, deps, runner, cfg, environmentv1.Tier_TIER_PREVIEW, "ocel bootstrap preview", out)
}

func preflightPreviewUp(ctx context.Context, deps cmddeps.Deps, runner *provider.Runner, cfg *projectconfig.Config, pointer string, out io.Writer, stdin io.Reader) error {
	resp, err := preflight(ctx, deps, runner, cfg, environmentv1.Tier_TIER_PREVIEW, cfg.Slug, declaredHostnames(cfg, "preview"), projectFrameworks(cfg), "ocel bootstrap preview", out)
	if err != nil {
		return err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims(), filepath.Base(cfg.Path)); err != nil {
		return err
	}
	if err := bootstrap.Offer(ctx, runner, resp.GetBootstrap(), environmentv1.Tier_TIER_PREVIEW, edgewire.Selection(cfg), deps.StdinIsTerminal(stdin), out); err != nil {
		return err
	}
	return requirePreviewDomain(cfg, resp.GetPreviewWildcard(), resp.GetIdentity(), pointer, out)
}

func preflightDeploy(ctx context.Context, deps cmddeps.Deps, runner *provider.Runner, cfg *projectconfig.Config, interactive bool, out io.Writer, stdin io.Reader) ([]string, error) {
	domains := declaredHostnames(cfg, "production")
	var slug string
	if interactive || len(domains) > 0 {
		slug = cfg.Slug
	}
	resp, err := preflight(ctx, deps, runner, cfg, environmentv1.Tier_TIER_PRODUCTION, slug, domains, projectFrameworks(cfg), "ocel bootstrap production", out)
	if err != nil {
		return nil, err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims(), filepath.Base(cfg.Path)); err != nil {
		return nil, err
	}
	if err := bootstrap.Offer(ctx, runner, resp.GetBootstrap(), environmentv1.Tier_TIER_PRODUCTION, edgewire.Selection(cfg), interactive, out); err != nil {
		return nil, err
	}
	return resp.GetKnownSlugs(), nil
}

func declaredHostnames(cfg *projectconfig.Config, class string) []string {
	var hosts []string
	seen := map[string]bool{}
	add := func(domains map[string][]string) {
		for _, host := range domains[class] {
			if seen[host] {
				continue
			}
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	add(cfg.Domains)
	for _, app := range cfg.Apps {
		add(app.Domains)
	}
	return hosts
}

func refuseClaimedDomains(claims []*contractv1.DomainClaim, configName string) error {
	var b strings.Builder
	for _, claim := range claims {
		if claim.GetStatus() != contractv1.DomainClaim_STATUS_CLAIMED {
			continue
		}
		if claim.GetOwner() == edge.PreviewEntryOwner {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("another project already serves a hostname this project declares:")
		}
		fmt.Fprintf(&b, "\n  ✗ %s is held by %s", claim.GetHostname(), claim.GetOwner())
	}
	if b.Len() == 0 {
		return nil
	}
	b.WriteString("\n    → a hostname belongs to one project, so deploying would take it over: remove it from this project's " +
		configName + ", or tear the owning project down (`ocel destroy production` / `ocel destroy preview` in it), then deploy again")
	return errors.New(b.String())
}

func preflightTier(ctx context.Context, deps cmddeps.Deps, runner *provider.Runner, cfg *projectconfig.Config, required environmentv1.Tier, bootstrapHint string, out io.Writer) error {
	resp, err := preflight(ctx, deps, runner, cfg, required, "", nil, projectFrameworks(cfg), bootstrapHint, out)
	if err != nil {
		return err
	}
	return bootstrap.PlanFor(resp.GetBootstrap()).Refusal(required)
}

func preflightCredentials(ctx context.Context, deps cmddeps.Deps, runner *provider.Runner, cfg *projectconfig.Config, required environmentv1.Tier, bootstrapHint string, out io.Writer) error {
	_, err := preflight(ctx, deps, runner, cfg, required, "", nil, nil, bootstrapHint, out)
	return err
}

func preflight(ctx context.Context, deps cmddeps.Deps, runner *provider.Runner, cfg *projectconfig.Config, required environmentv1.Tier, slug string, domains []string, frameworks []string, bootstrapHint string, out io.Writer) (*contractv1.PreflightResponse, error) {
	client, err := runner.Client()
	if err != nil {
		return nil, err
	}

	spinner := deployui.StartSpinner(out, "Checking credentials")
	resp, err := client.Preflight(ctx, &contractv1.PreflightRequest{
		RequiredTier: required,
		Slug:         slug,
		Domains:      domains,
		Frameworks:   frameworks,
		Edge:         edgewire.Selection(cfg),
	})
	spinner.Stop()
	if err != nil {
		return nil, err
	}
	if deps.StdoutIsTerminal(out) {
		fmt.Fprint(out, formatIdentityBanner(resp.GetIdentity()))
	}
	if err := credentialProblems(resp.GetCredentialProblems()); err != nil {
		return nil, err
	}
	if !resp.GetInfrastructurePresent() {
		return nil, fmt.Errorf("no infrastructure is set up yet; run `%s` to create it", bootstrapHint)
	}
	if err := checkTier(resp.GetInfraTier(), required); err != nil {
		return nil, err
	}
	return resp, nil
}

func formatIdentityBanner(id *contractv1.Identity) string {
	if id == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Running with:\n")
	wrote := false
	if line := originIdentityLine(id); line != "" {
		fmt.Fprintf(&b, "  %-11s %s\n", id.GetProvider(), line)
		wrote = true
	}
	if scope := id.GetEdgeScope(); scope != "" {
		fmt.Fprintf(&b, "  %-11s account=%s\n", "Edge", scope)
		wrote = true
	}
	if !wrote {
		return ""
	}
	return b.String()
}

func originIdentityLine(id *contractv1.Identity) string {
	if id.GetAccount() == "" {
		return ""
	}
	parts := []string{"account=" + id.GetAccount()}
	if principal := id.GetPrincipal(); principal != "" {
		parts = append(parts, "identity="+principal)
	}
	for _, detail := range id.GetDetails() {
		parts = append(parts, detail.GetLabel()+"="+detail.GetValue())
	}
	return strings.Join(parts, "  ")
}

func credentialProblems(problems []*contractv1.CredentialProblem) error {
	if len(problems) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("credential check failed:")
	for _, p := range problems {
		fmt.Fprintf(&b, "\n  ✗ %s: %s", p.GetProvider(), p.GetMessage())
		if h := p.GetHint(); h != "" {
			fmt.Fprintf(&b, "\n    → %s", h)
		}
	}
	return errors.New(b.String())
}

func renderEnvironments(stdout io.Writer, envs []*contractv1.PreviewEnvironment) {
	if len(envs) == 0 {
		fmt.Fprintln(stdout, "No preview environments.")
		return
	}
	for _, e := range envs {
		fmt.Fprintf(stdout, "%s\t%s\t%s\tcreated %s\texpires %s\n",
			e.GetIdentity(),
			lifecycleTag(e.GetLifecycle()),
			labelOrDash(e.GetLabel()),
			epochDate(e.GetCreatedAt()),
			epochDate(e.GetExpiresAt()),
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

func epochDate(sec int64) string {
	if sec == 0 {
		return "—"
	}
	return time.Unix(sec, 0).UTC().Format("2006-01-02")
}
