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
	"github.com/ocelhq/ocel/cli/internal/deployresult"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
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
	yes      bool
	dry      bool
}

type previewRmOptions struct {
	ref  string
	name string
	yes  bool
}

type previewPruneOptions struct {
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
			ctx, stop := deps.Interrupt(cmd.Context(), cmd.ErrOrStderr())
			defer stop()
			return runPreviewRm(ctx, deps, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
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
			ctx, stop := deps.Interrupt(cmd.Context(), cmd.ErrOrStderr())
			defer stop()
			return runPreviewLs(ctx, deps, cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}

	var pruneOpts previewPruneOptions
	prune := &cobra.Command{
		Use:   "prune --name <preview>",
		Short: "Delete a named preview's old deployments",
		Long: "Delete a named preview's old deployments.\n\n" +
			"Keeps the newest --keep deployments and whatever is live. Only named previews are pruned; " +
			"a branch's preview goes down whole with `ocel preview rm`.",
		Example: "  $ ocel preview prune --name staging\n" +
			"  $ ocel preview prune --name staging --keep 5",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			opts := pruneOpts
			ctx, stop := deps.Interrupt(cmd.Context(), cmd.ErrOrStderr())
			defer stop()
			return runPreviewPrune(ctx, deps, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}
	prune.Flags().StringVar(&pruneOpts.name, "name", "", "The named preview to prune")
	prune.Flags().IntVar(&pruneOpts.keep, "keep", defaultPreviewPruneKeepN, "How many recent deployments to keep (the live one always stays)")
	cmddeps.Yes(prune, &pruneOpts.yes)
	_ = prune.MarkFlagRequired("name")

	cmd.AddCommand(up, rm, ls, prune)
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
		ctx, stop := deps.Interrupt(cmd.Context(), cmd.ErrOrStderr())
		defer stop()
		return runPreviewUp(ctx, deps, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	}
}

func runPreviewUp(ctx context.Context, deps cmddeps.Deps, cwd string, opts previewUpOptions, stdout, stderr io.Writer, stdin io.Reader) error {
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

	spec := deps.Spec(runui.Convergent, "ocel preview up", cfg, opts.yes, stdout, stdin)
	spec.Dry = opts.dry

	return runui.Run(ctx, spec, func(ctx context.Context, runner *provider.Runner, ui *runui.Session) error {
		knownSlugs, compute, err := preflightPreviewUp(ctx, deps, ui, runner, cfg, env.GetIdentity(), stdout, stdin)
		if err != nil {
			return err
		}

		proceed, err := guardNewProject(ctx, ui, cfg, knownSlugs)
		if err != nil || !proceed {
			return err
		}

		ui.Building()
		recovery := gateRecovery{
			deps:    deps,
			cfg:     cfg,
			runner:  runner,
			preview: true,
			newGate: func() *envgate.Gate {
				return envgate.New(envwire.Values{
					Runner: runner,
					Slug:   cfg.Slug,
					Tier:   environmentv1.Tier_TIER_PREVIEW,
				}, envwire.Scope(cfg, true, env.GetIdentity()))
			},
			compute: compute,
			ui:      ui,
			enabled: !opts.dry && canOpenVarsUI(deps, stdin),
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
			Dry:         opts.dry,
		}

		if opts.dry {
			return showDeployPlan(ctx, runner, ui, req, fmt.Sprintf("Proposed changes to preview %s", env.GetIdentity()))
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

func requirePreviewDomain(cfg *projectconfig.Config, wildcard *contractv1.PreviewWildcard, id *contractv1.Identity, pointer string, rep runui.Reporter) error {
	declared := ""
	if hosts := preflight.Hostnames(cfg, "preview"); len(hosts) > 0 {
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
		rep.Diagnostic(fmt.Sprintf("Serving previews on the global preview domain *.%s — this project declares no domains.preview of its own", base))

	case base != "":
		rep.Diagnostic(fmt.Sprintf("Serving previews on %s, this project's own domains.preview — the global preview domain *.%s exists and is ignored (remove domains.preview from %s to serve on it)",
			declared, base, configName))
	}

	site := edge.ProjectPreview(strings.TrimPrefix(declared, "*."))
	if declared == "" {
		site = edge.SharedPreview(cfg.Slug, base)
	}
	return site.LabelProblem(site.Hosts(pointer, previewAppNames(cfg)))
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
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}

	env, err := resolveRmEnvironment(deps, cwd, opts)
	if err != nil {
		return err
	}

	persistent := env.GetLifecycle() == environmentv1.Lifecycle_LIFECYCLE_PERSISTENT

	return runui.Run(ctx, deps.Spec(runui.Convergent, "ocel preview rm", cfg, opts.yes, stdout, stdin), func(ctx context.Context, runner *provider.Runner, ui *runui.Session) error {
		if persistent {
			proceed, err := ui.Guard(ctx, fmt.Sprintf("Tear down the named preview %q?", env.GetIdentity()))
			if err != nil {
				if ctx.Err() != nil {
					fmt.Fprintln(stdout, "Interrupted.")
					return &exitsig.ExitError{Code: exitsig.InterruptCode}
				}
				return err
			}
			if !proceed {
				return nil
			}
		}

		if err := preflightPreview(ctx, ui, runner, cfg); err != nil {
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
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}

	return provider.Drive(ctx, cfg, stdout, stderr, deps.HostTrust, func(runner *provider.Runner) error {
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

func runPreviewPrune(ctx context.Context, deps cmddeps.Deps, cwd string, opts previewPruneOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	if opts.name == "" {
		return fmt.Errorf("`ocel preview prune` needs --name: only named previews are pruned")
	}
	env, err := persistentPreviewEnvironment(opts.name)
	if err != nil {
		return err
	}

	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}

	return runui.Run(ctx, deps.Spec(runui.Convergent, "ocel preview prune", cfg, opts.yes, stdout, stdin), func(ctx context.Context, runner *provider.Runner, ui *runui.Session) error {
		if err := preflightPreview(ctx, ui, runner, cfg); err != nil {
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

func resolveRmEnvironment(deps cmddeps.Deps, cwd string, opts previewRmOptions) (*environmentv1.Environment, error) {
	if opts.name != "" && opts.ref != "" {
		return nil, fmt.Errorf("pass --name or --ref, not both: a preview is either named or a branch's")
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
		Label:     id.Label,
	}, nil
}

func renderEnvironments(stdout io.Writer, envs []*contractv1.PreviewEnvironment) {
	if len(envs) == 0 {
		fmt.Fprintln(stdout, "No previews.")
		return
	}
	for _, e := range envs {
		fmt.Fprintf(stdout, "%s\t%s\t%s\tcreated %s\texpires %s\n",
			e.GetIdentity(),
			lifecycleTag(e.GetLifecycle()),
			labelOrDash(e.GetLabel()),
			runui.EpochDate(e.GetCreatedAt()),
			runui.EpochDate(e.GetExpiresAt()),
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
