package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/deployresult"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/node"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
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
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runPreviewRm(ctx, defaultDeps(), cwd, previewRmOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
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
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runPreviewLs(ctx, defaultDeps(), cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func runPreviewUpCmd(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runPreviewUp(ctx, defaultDeps(), cwd, previewUpOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
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
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runPreviewPrune(ctx, defaultDeps(), cwd, previewPruneOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
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

func runPreviewUp(ctx context.Context, d deps, cwd string, opts previewUpOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

	if err := node.Ensure(cfg.Dir); err != nil {
		return err
	}
	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	env, err := resolveUpEnvironment(d, cwd, opts)
	if err != nil {
		return err
	}

	if err := deployresult.Clear(cfg.Dir); err != nil {
		return err
	}

	ctx, run, err := startRun(ctx, cfg, "ocel preview up")
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sessionFormat(), verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, d, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		if err := preflightPreviewUp(ctx, d, runner, provider, cfg, env.GetIdentity(), stdout); err != nil {
			return err
		}

		ui.Building()
		recovery := gateRecovery{
			deps:     d,
			cfg:      cfg,
			provider: provider,
			runner:   runner,
			preview:  true,
			newGate: func() *envgate.Gate {
				return envgate.New(runnerValues{
					runner:  runner,
					options: []byte(provider.Options),
					slug:    cfg.Slug,
					class:   deploymentsv1.Environment_CLASS_PREVIEW,
				}, envScope(cfg, true, env.GetIdentity()))
			},
			ui:      ui,
			stdout:  stdout,
			enabled: d.canOpenVarsUI(stdin, opts.noUI),
		}
		manifest, err := recovery.buildManifest(ctx, opts.prebuilt, ui.BuildWriter())
		if err != nil {
			return err
		}
		if manifest == nil {
			ui.Finish("Nothing to deploy")
			return nil
		}
		ui.BuildOK()

		req := &deploymentsv1.DeployRequest{
			Manifest:        manifest,
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Environment:     env,
		}

		var stackOutputs []*deploymentsv1.ResourceOutput
		var appURLs []string
		var promotionID string
		onEvent := func(ev *deploymentsv1.DeployEvent) {
			ui.Event(ev)
			if res := ev.GetResult(); res != nil {
				stackOutputs = res.GetOutputs()
				appURLs = res.GetAppUrls()
				promotionID = res.GetPromotionId()
			}
		}
		if err := runner.Deploy(ctx, req, onEvent); err != nil {
			return err
		}

		if err := recordDeployResult(cfg, manifest, env, "", promotionID, appURLs); err != nil {
			return err
		}
		ui.Deployed(fmt.Sprintf("Preview %s is up", env.GetIdentity()), appURLs, stackOutputs)
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

func requirePreviewDomain(cfg *projectconfig.Config, global *deploymentsv1.GlobalPreviewDomain, id *deploymentsv1.Identity, pointer string, out io.Writer) error {
	declared := ""
	if hosts := declaredHostnames(cfg, "preview"); len(hosts) > 0 {
		declared = hosts[0]
	}
	base := global.GetBaseDomain()

	switch {
	case declared == "" && base == "":
		return fmt.Errorf("this project declares no preview domain and this substrate has no global one, so a preview deploy has nowhere to serve: "+
			"add a project-level domains.preview wildcard (e.g. `domains: { preview: \"*.preview.acme.com\" }`) to %s, "+
			"or run `ocel domain use '*.preview.acme.com' --preview` once to serve every project's previews on one shared wildcard — "+
			"a preview domain binds to the whole project, which serves every app and every preview under that one wildcard, so it is never declared per app",
			projectconfig.ConfigFileName)

	case declared == "":
		if err := checkGlobalPreviewDomain(global, id); err != nil {
			return err
		}
		fmt.Fprintf(out, "Serving previews on the global preview domain *.%s — this project declares no domains.preview of its own\n", base)

	case base != "":
		fmt.Fprintf(out, "Serving previews on %s, this project's own domains.preview — the global preview domain *.%s exists and is ignored (remove domains.preview from %s to serve on it)\n",
			declared, base, projectconfig.ConfigFileName)
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
		return []string{edge.PreviewLabel(slug, pointer, "") + "." + base}
	}
	hosts := make([]string, 0, len(cfg.Apps))
	for _, app := range cfg.Apps {
		hosts = append(hosts, edge.PreviewLabel(slug, pointer, app.Name)+"."+base)
	}
	return hosts
}

func checkGlobalPreviewDomain(global *deploymentsv1.GlobalPreviewDomain, id *deploymentsv1.Identity) error {
	base := global.GetBaseDomain()
	if want, have := global.GetCloudflareAccount(), id.GetCloudflareAccount(); want != "" && have != "" && want != have {
		return fmt.Errorf("the global preview domain *.%s lives in Cloudflare account %s, but this deploy is authenticated to account %s: "+
			"a worker route can only be attached from the account that holds the zone — "+
			"point CLOUDFLARE_ACCOUNT_ID (and CLOUDFLARE_API_TOKEN) at %s, or declare this project's own domains.preview in %s",
			base, want, have, want, projectconfig.ConfigFileName)
	}
	if !global.GetRouteInstalled() {
		return fmt.Errorf("the global preview domain *.%s is recorded, but its wildcard route is not installed, so nothing would answer a preview hostname: "+
			"run `ocel domain use '*.%s' --preview` to reinstall the shared entry worker and reclaim the wildcard",
			base, base)
	}
	if g := edge.PreviewGrammarMax; g < global.GetGrammarMin() || g > global.GetGrammarMax() {
		return fmt.Errorf("this CLI names preview hostnames with grammar %d, but the shared entry worker on *.%s speaks %d–%d, so it would not route what this deploy creates: "+
			"run `ocel domain use '*.%s' --preview` to upgrade the worker, or upgrade the CLI if it is the older half",
			g, base, global.GetGrammarMin(), global.GetGrammarMax(), base)
	}
	return nil
}

func runPreviewRm(ctx context.Context, d deps, cwd string, opts previewRmOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

	if err := node.Ensure(cfg.Dir); err != nil {
		return err
	}
	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	env, err := resolveRmEnvironment(d, cwd, opts)
	if err != nil {
		return err
	}

	persistent := env.GetLifecycle() == deploymentsv1.Environment_LIFECYCLE_PERSISTENT
	if persistent && !opts.yes && isReaderTTY(stdin) {
		proceed, err := confirmDestroyPreview(env.GetIdentity(), stdout, stdin)
		if err != nil {
			return err
		}
		if !proceed {
			fmt.Fprintln(stdout, "Aborted.")
			return nil
		}
	}

	ctx, run, err := startRun(ctx, cfg, "ocel preview rm")
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sessionFormat(), verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, d, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		if err := preflightPreview(ctx, d, runner, provider, stdout); err != nil {
			return err
		}

		req := &deploymentsv1.DestroyPreviewRequest{
			Environment:     env,
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Slug:            cfg.Slug,
		}
		if err := runner.DestroyPreview(ctx, req, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Preview %s torn down", env.GetIdentity()))
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

func runPreviewLs(ctx context.Context, d deps, cwd string, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

	if err := node.Ensure(cfg.Dir); err != nil {
		return err
	}
	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	return runProviderSession(ctx, d, cfg, provider, stdout, stderr, func(runner *providerrunner.Runner) error {
		client, err := runner.Deployments()
		if err != nil {
			return err
		}
		resp, err := client.ListEnvironments(ctx, &deploymentsv1.ListEnvironmentsRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Slug:            cfg.Slug,
		})
		if err != nil {
			return err
		}
		renderEnvironments(stdout, resp.GetEnvironments())
		return nil
	})
}

func runPreviewPrune(ctx context.Context, d deps, cwd string, opts previewPruneOptions, stdout, stderr io.Writer) error {
	if opts.name == "" {
		return fmt.Errorf("`ocel preview prune` requires --name: only persistent previews are pruned")
	}
	env, err := persistentPreviewEnvironment(opts.name)
	if err != nil {
		return err
	}

	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

	if err := node.Ensure(cfg.Dir); err != nil {
		return err
	}
	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	ctx, run, err := startRun(ctx, cfg, "ocel preview prune")
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sessionFormat(), verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, d, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		if err := preflightPreview(ctx, d, runner, provider, stdout); err != nil {
			return err
		}
		if err := runner.Prune(ctx, &deploymentsv1.PruneRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Slug:            cfg.Slug,
			KeepN:           int32(opts.keep),
			Environment:     env,
		}, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Pruned preview %q", opts.name))
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

func persistentPreviewEnvironment(name string) (*deploymentsv1.Environment, error) {
	if err := previewid.ValidateLabel(name); err != nil {
		return nil, err
	}
	return &deploymentsv1.Environment{
		Class:          deploymentsv1.Environment_CLASS_PREVIEW,
		Lifecycle:      deploymentsv1.Environment_LIFECYCLE_PERSISTENT,
		Identity:       name,
		IdentitySource: deploymentsv1.Environment_IDENTITY_SOURCE_DECLARED,
	}, nil
}

func resolveUpEnvironment(d deps, cwd string, opts previewUpOptions) (*deploymentsv1.Environment, error) {
	if opts.name != "" && opts.ref != "" {
		return nil, fmt.Errorf("--name and --ref are mutually exclusive; use one to stand up a persistent or ephemeral preview")
	}
	if opts.name != "" {
		return persistentPreviewEnvironment(opts.name)
	}

	ref, prNumber := opts.ref, ""
	if ref == "" {
		branch, err := d.currentGitBranch(cwd)
		if err != nil {
			return nil, err
		}
		ref, prNumber = branch, d.discoverPRNumber()
	}
	id, err := previewid.Resolve(ref, prNumber)
	if err != nil {
		return nil, err
	}
	return &deploymentsv1.Environment{
		Class:          deploymentsv1.Environment_CLASS_PREVIEW,
		Lifecycle:      deploymentsv1.Environment_LIFECYCLE_EPHEMERAL,
		Identity:       id.Key,
		IdentitySource: deploymentsv1.Environment_IDENTITY_SOURCE_GIT,
		Label:          id.Label,
	}, nil
}

func resolveRmEnvironment(d deps, cwd string, opts previewRmOptions) (*deploymentsv1.Environment, error) {
	if opts.name != "" && opts.ref != "" {
		return nil, fmt.Errorf("--name and --ref are mutually exclusive; use one to address a persistent or ephemeral preview")
	}
	if opts.name != "" {
		return persistentPreviewEnvironment(opts.name)
	}

	ref := opts.ref
	if ref == "" {
		branch, err := d.currentGitBranch(cwd)
		if err != nil {
			return nil, err
		}
		ref = branch
	}
	id, err := previewid.Resolve(ref, "")
	if err != nil {
		return nil, err
	}
	return &deploymentsv1.Environment{
		Class:          deploymentsv1.Environment_CLASS_PREVIEW,
		Lifecycle:      deploymentsv1.Environment_LIFECYCLE_EPHEMERAL,
		Identity:       id.Key,
		IdentitySource: deploymentsv1.Environment_IDENTITY_SOURCE_GIT,
	}, nil
}

func confirmDestroyPreview(name string, stdout io.Writer, stdin io.Reader) (bool, error) {
	return confirmYN(fmt.Sprintf("Destroy persistent preview %q?", name), stdout, stdin)
}

func preflightPreview(ctx context.Context, d deps, runner *providerrunner.Runner, provider *projectconfig.ProviderDescriptor, out io.Writer) error {
	return preflightClass(ctx, d, runner, provider, deploymentsv1.Environment_CLASS_PREVIEW, "ocel bootstrap --preview", out)
}

func preflightPreviewUp(ctx context.Context, d deps, runner *providerrunner.Runner, provider *projectconfig.ProviderDescriptor, cfg *projectconfig.Config, pointer string, out io.Writer) error {
	resp, err := preflight(ctx, d, runner, provider, deploymentsv1.Environment_CLASS_PREVIEW, cfg.Slug, declaredHostnames(cfg, "preview"), "ocel bootstrap --preview", out)
	if err != nil {
		return err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims()); err != nil {
		return err
	}
	return requirePreviewDomain(cfg, resp.GetGlobalPreviewDomain(), resp.GetIdentity(), pointer, out)
}

func preflightDeploy(ctx context.Context, d deps, runner *providerrunner.Runner, provider *projectconfig.ProviderDescriptor, cfg *projectconfig.Config, wantKnownSlugs bool, out io.Writer) ([]string, error) {
	domains := declaredHostnames(cfg, "production")
	var slug string
	if wantKnownSlugs || len(domains) > 0 {
		slug = cfg.Slug
	}
	resp, err := preflight(ctx, d, runner, provider, deploymentsv1.Environment_CLASS_PRODUCTION, slug, domains, "ocel bootstrap", out)
	if err != nil {
		return nil, err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims()); err != nil {
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

func refuseClaimedDomains(claims []*deploymentsv1.DomainClaim) error {
	var b strings.Builder
	for _, claim := range claims {
		if claim.GetStatus() != deploymentsv1.DomainClaim_STATUS_CLAIMED {
			continue
		}
		if claim.GetOwner() == edge.SharedPreviewEntryScript {
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
		projectconfig.ConfigFileName + ", or tear the owning project down (`ocel destroy` / `ocel destroy --preview` in it), then deploy again")
	return errors.New(b.String())
}

func preflightClass(ctx context.Context, d deps, runner *providerrunner.Runner, provider *projectconfig.ProviderDescriptor, required deploymentsv1.Environment_Class, bootstrapHint string, out io.Writer) error {
	_, err := preflight(ctx, d, runner, provider, required, "", nil, bootstrapHint, out)
	return err
}

func preflight(ctx context.Context, d deps, runner *providerrunner.Runner, provider *projectconfig.ProviderDescriptor, required deploymentsv1.Environment_Class, slug string, domains []string, bootstrapHint string, out io.Writer) (*deploymentsv1.PreflightResponse, error) {
	client, err := runner.Deployments()
	if err != nil {
		return nil, err
	}

	spinner := deployui.StartSpinner(out, "Checking credentials")
	resp, err := client.Preflight(ctx, &deploymentsv1.PreflightRequest{
		Options:         []byte(provider.Options),
		ProtocolVersion: manifestbuilder.SchemaVersion,
		RequiredClass:   required,
		Slug:            slug,
		Domains:         domains,
	})
	spinner.Stop()
	if err != nil {
		return nil, err
	}
	if d.stdoutIsTerminal(out) {
		fmt.Fprint(out, formatIdentityBanner(resp.GetIdentity()))
	}
	if err := credentialProblems(resp.GetCredentialProblems()); err != nil {
		return nil, err
	}
	if !resp.GetInfrastructurePresent() {
		return nil, fmt.Errorf("no infrastructure is set up yet; run `%s` to create it", bootstrapHint)
	}
	if err := deploymentsv1.CheckClass(resp.GetInfraClass(), required); err != nil {
		return nil, err
	}
	return resp, nil
}

func formatIdentityBanner(id *deploymentsv1.Identity) string {
	if id == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Running with:\n")
	wrote := false
	if line := awsIdentityLine(id); line != "" {
		fmt.Fprintf(&b, "  AWS         %s\n", line)
		wrote = true
	}
	if acct := id.GetCloudflareAccount(); acct != "" {
		fmt.Fprintf(&b, "  Cloudflare  account=%s\n", acct)
		wrote = true
	}
	if !wrote {
		return ""
	}
	return b.String()
}

func awsIdentityLine(id *deploymentsv1.Identity) string {
	if id.GetAwsAccount() == "" {
		return ""
	}
	var parts []string
	if p := id.GetAwsProfile(); p != "" {
		parts = append(parts, "profile="+p)
	} else if arn := id.GetAwsArn(); arn != "" {
		parts = append(parts, "identity="+arnPrincipal(arn))
	}
	parts = append(parts, "account="+id.GetAwsAccount())
	if r := id.GetAwsRegion(); r != "" {
		parts = append(parts, "region="+r)
	}
	return strings.Join(parts, "  ")
}

func arnPrincipal(arn string) string {
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		return arn[i+1:]
	}
	if i := strings.LastIndex(arn, ":"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

func credentialProblems(problems []*deploymentsv1.CredentialProblem) error {
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

func renderEnvironments(stdout io.Writer, envs []*deploymentsv1.PreviewEnvironment) {
	if len(envs) == 0 {
		fmt.Fprintln(stdout, "No preview environments.")
		return
	}
	for _, e := range envs {
		fmt.Fprintf(stdout, "%s\t%s\t%s\tcreated %s\texpires %s\n",
			e.GetIdentity(),
			lifecycleTag(e.GetLifecycle()),
			labelOrDash(e.GetLabel()),
			epochOrDash(e.GetCreatedAt()),
			epochOrDash(e.GetExpiresAt()),
		)
	}
}

func lifecycleTag(l deploymentsv1.Environment_Lifecycle) string {
	switch l {
	case deploymentsv1.Environment_LIFECYCLE_EPHEMERAL:
		return "ephemeral"
	case deploymentsv1.Environment_LIFECYCLE_PERSISTENT:
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

func epochOrDash(sec int64) string {
	if sec == 0 {
		return "—"
	}
	return time.Unix(sec, 0).UTC().Format("2006-01-02")
}
