package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/node"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type domainOptions struct {
	preview bool
	yes     bool
}

var domainOpts domainOptions

var domainCmd = &cobra.Command{
	Use:   "domain",
	Short: "Manage the substrate-wide domain every project's previews are served on",
	Long: "Manage the substrate-wide domain every project's previews are served on.\n\n" +
		"One shared entry worker on one wildcard serves every project bootstrapped into the " +
		"preview class, at \"<project>--<preview>[--<app>].<domain>\". A project that declares " +
		"its own domains.preview keeps it and ignores this one.",
	Args: cobra.NoArgs,
}

var domainUseCmd = &cobra.Command{
	Use:   "use <wildcard>",
	Short: "Install (or upgrade) the shared entry worker and serve every project's previews on this wildcard",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runDomainUse(ctx, defaultDeps(), cwd, args[0], domainOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var domainLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "Show the global domain and the projects served on it",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runDomainLs(ctx, defaultDeps(), cwd, domainOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var domainReleaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Tear down the shared entry worker and stop serving previews on the global domain",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runDomainRelease(ctx, defaultDeps(), cwd, domainOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func init() {
	for _, c := range []*cobra.Command{domainUseCmd, domainLsCmd, domainReleaseCmd} {
		c.Flags().BoolVar(&domainOpts.preview, "preview", false, "Act on the preview class (required)")
	}
	domainReleaseCmd.Flags().BoolVarP(&domainOpts.yes, "yes", "y", false, "Skip the typed confirmation, for CI")

	domainCmd.AddCommand(domainUseCmd)
	domainCmd.AddCommand(domainLsCmd)
	domainCmd.AddCommand(domainReleaseCmd)
	rootCmd.AddCommand(domainCmd)
}

func requirePreviewClass(command string, preview bool) error {
	if preview {
		return nil
	}
	return fmt.Errorf("`%s` needs --preview: a global domain is preview-only — a production hostname belongs to one project and is declared in that project's %s, so there is no global production domain to manage",
		command, projectconfig.ConfigFileName)
}

func globalPreviewBaseDomain(wildcard string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(wildcard))
	if err := projectconfig.ValidatePreviewDomain(host); err != nil {
		return "", err
	}
	return projectconfig.PreviewBaseDomain(host), nil
}

func runDomainUse(ctx context.Context, d deps, cwd, wildcard string, opts domainOptions, stdout, stderr io.Writer) error {
	if err := requirePreviewClass("ocel domain use", opts.preview); err != nil {
		return err
	}
	base, err := globalPreviewBaseDomain(wildcard)
	if err != nil {
		return err
	}

	cfg, provider, err := domainSession(cwd)
	if err != nil {
		return err
	}

	ctx, run, err := startRun(ctx, cfg, "ocel domain use")
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
		req := &deploymentsv1.UseDomainRequest{
			Class:      deploymentsv1.Environment_CLASS_PREVIEW,
			BaseDomain: base,
		}
		if err := runner.UseDomain(ctx, req, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Previews are served on %s", wildcardOf(base)))
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

func runDomainLs(ctx context.Context, d deps, cwd string, opts domainOptions, stdout, stderr io.Writer) error {
	if err := requirePreviewClass("ocel domain ls", opts.preview); err != nil {
		return err
	}

	cfg, provider, err := domainSession(cwd)
	if err != nil {
		return err
	}

	return runProviderSession(ctx, d, cfg, provider, stdout, stderr, func(runner *providerrunner.Runner) error {
		resp, err := listGlobalPreviewDomain(ctx, d, runner, provider, stdout)
		if err != nil {
			return err
		}
		renderGlobalDomain(stdout, resp)
		return nil
	})
}

func runDomainRelease(ctx context.Context, d deps, cwd string, opts domainOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	if err := requirePreviewClass("ocel domain release", opts.preview); err != nil {
		return err
	}
	if !opts.yes && !isReaderTTY(stdin) {
		return errors.New("`ocel domain release --preview` needs an interactive terminal to confirm the domain; re-run with --yes to release it non-interactively")
	}

	cfg, provider, err := domainSession(cwd)
	if err != nil {
		return err
	}

	ctx, run, err := startRun(ctx, cfg, "ocel domain release")
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sessionFormat(), verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, d, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		resp, err := listGlobalPreviewDomain(ctx, d, runner, provider, stdout)
		if err != nil {
			return err
		}
		base := resp.GetDomain().GetBaseDomain()
		if base == "" {
			ui.Finish("No global preview domain is configured")
			return nil
		}

		fmt.Fprintf(stdout, "This will tear down the shared entry worker on %s and release the wildcard.\n", wildcardOf(base))
		renderGlobalDomainProjects(stdout, resp.GetProjects())
		fmt.Fprintln(stdout, "Every project above keeps its previews deployed, but they stop being reachable until a global domain is in use again or each project declares its own domains.preview.")

		if !opts.yes {
			confirmed, err := confirmReleaseDomain(base, stdout, stdin)
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		req := &deploymentsv1.ReleaseDomainRequest{Class: deploymentsv1.Environment_CLASS_PREVIEW}
		if err := runner.ReleaseDomain(ctx, req, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Released %s", wildcardOf(base)))
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

func domainSession(cwd string) (*projectconfig.Config, *projectconfig.ProviderDescriptor, error) {
	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return nil, nil, err
	}
	if err := node.Ensure(cfg.Dir); err != nil {
		return nil, nil, err
	}
	provider, err := cfg.RequireProvider()
	if err != nil {
		return nil, nil, err
	}
	return cfg, provider, nil
}

func listGlobalPreviewDomain(ctx context.Context, d deps, runner *providerrunner.Runner, provider *projectconfig.ProviderDescriptor, out io.Writer) (*deploymentsv1.ListDomainResponse, error) {
	if err := preflightPreview(ctx, d, runner, provider, out); err != nil {
		return nil, err
	}
	client, err := runner.Deployments()
	if err != nil {
		return nil, err
	}
	spinner := deployui.StartSpinner(out, "Reading the global preview domain")
	resp, err := client.ListDomain(ctx, &deploymentsv1.ListDomainRequest{Class: deploymentsv1.Environment_CLASS_PREVIEW})
	spinner.Stop()
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func renderGlobalDomain(out io.Writer, resp *deploymentsv1.ListDomainResponse) {
	domain := resp.GetDomain()
	if domain.GetBaseDomain() == "" {
		fmt.Fprintln(out, "No global preview domain is configured.")
		fmt.Fprintln(out, "  → run `ocel domain use '*.preview.example.com' --preview` to serve every project's previews on one wildcard")
		return
	}

	fmt.Fprintf(out, "Global preview domain  %s\n", wildcardOf(domain.GetBaseDomain()))
	if acct := domain.GetCloudflareAccount(); acct != "" {
		fmt.Fprintf(out, "  Cloudflare account   %s\n", acct)
	}
	fmt.Fprintf(out, "  Hostname grammar     %d–%d\n", domain.GetGrammarMin(), domain.GetGrammarMax())
	route := "installed"
	if !domain.GetRouteInstalled() {
		route = "MISSING — run `ocel domain use '" + wildcardOf(domain.GetBaseDomain()) + "' --preview` to reinstall it"
	}
	fmt.Fprintf(out, "  Wildcard route       %s\n", route)
	renderGlobalDomainProjects(out, resp.GetProjects())
}

func renderGlobalDomainProjects(out io.Writer, projects []string) {
	if len(projects) == 0 {
		fmt.Fprintln(out, "No project is bootstrapped into the preview class yet.")
		return
	}
	fmt.Fprintf(out, "Projects served (%d):\n", len(projects))
	for _, p := range projects {
		fmt.Fprintf(out, "  • %s\n", p)
	}
}

func confirmReleaseDomain(base string, stdout io.Writer, stdin io.Reader) (bool, error) {
	fmt.Fprintf(stdout, "Type the domain (%s) to confirm: ", base)

	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, fmt.Errorf("failed to read input: %w", err)
		}
		return false, nil
	}
	return strings.TrimSpace(scanner.Text()) == base, nil
}

func wildcardOf(base string) string {
	return "*." + base
}
