package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/node"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
)

type domainOptions struct {
	preview bool
	yes     bool
	wait    bool
}

var domainOpts domainOptions

var domainCmd = &cobra.Command{
	Use:   "domain",
	Short: "Manage this project's production hostnames, and the domain every project's previews are served on",
	Long: "Manage this project's production hostnames, and the substrate-wide domain every project's previews are served on.\n\n" +
		"`add` and `rm` are project-scoped and read domains.production, which is the declaration: " +
		"no command edits it. `use`, `ls` and `release` take --preview and act on the substrate, where " +
		"one shared entry worker on one wildcard serves every project bootstrapped into the preview " +
		"class, at \"<project>--<preview>[--<app>].<domain>\". A project that declares its own " +
		"domains.preview keeps it and ignores this one.",
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
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
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
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
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
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()
		return runDomainRelease(ctx, defaultDeps(), cwd, domainOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

var domainAddCmd = &cobra.Command{
	Use:   "add [host]",
	Short: "Provision the certificate, the edge surface and the DNS for this project's production hostnames",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()
		return runAddDomain(ctx, defaultDeps(), cwd, firstArg(args), cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var domainRmCmd = &cobra.Command{
	Use:   "rm [host]",
	Short: "Unbind production hostnames this project no longer declares, and remove what ocel created for them",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()
		return runDomainRm(ctx, defaultDeps(), cwd, firstArg(args), cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var domainStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show, per production hostname, its certificate, the records it needs, what last answered for it and what is still outstanding",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()
		return runDomainStatus(ctx, defaultDeps(), cwd, domainOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func init() {
	for _, c := range []*cobra.Command{domainUseCmd, domainLsCmd, domainReleaseCmd} {
		c.Flags().BoolVar(&domainOpts.preview, "preview", false, "Act on the preview class (required)")
	}
	domainReleaseCmd.Flags().BoolVarP(&domainOpts.yes, "yes", "y", false, "Skip the typed confirmation, for CI")
	domainStatusCmd.Flags().BoolVar(&domainOpts.wait, "wait", false, "Keep polling until every declared hostname is served, or give up")

	domainCmd.AddCommand(domainStatusCmd)
	domainCmd.AddCommand(domainAddCmd)
	domainCmd.AddCommand(domainRmCmd)
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

	cfg, provider, err := domainSession(ctx, cwd)
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
		if err := preflightPreview(ctx, d, runner, cfg, stdout); err != nil {
			return err
		}
		req := &deploymentsv1.UseDomainRequest{
			Class:      deploymentsv1.Environment_CLASS_PREVIEW,
			BaseDomain: base,
			Edge:       edgeSelection(cfg),
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

	cfg, provider, err := domainSession(ctx, cwd)
	if err != nil {
		return err
	}

	return runProviderSession(ctx, d, cfg, provider, stdout, stderr, func(runner *providerrunner.Runner) error {
		resp, err := listGlobalPreviewDomain(ctx, d, runner, cfg, stdout)
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

	cfg, provider, err := domainSession(ctx, cwd)
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
		if err := preflightPreview(ctx, d, runner, cfg, stdout); err != nil {
			return err
		}
		client, err := runner.Deployments()
		if err != nil {
			return err
		}

		spinner := deployui.StartSpinner(stdout, "Enumerating what releasing the domain would remove")
		plan, err := client.PlanReleaseDomain(ctx, &deploymentsv1.PlanReleaseDomainRequest{
			Class: deploymentsv1.Environment_CLASS_PREVIEW,
		})
		spinner.Stop()
		if err != nil {
			return err
		}
		base := plan.GetBaseDomain()
		if base == "" {
			ui.Finish("No global preview domain is configured")
			return nil
		}

		printReleasePlan(stdout, base, plan)

		if !opts.yes {
			confirmed, err := confirmPhrase(ctx, "domain", base, stdout, stdin)
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		req := &deploymentsv1.ReleaseDomainRequest{Class: deploymentsv1.Environment_CLASS_PREVIEW, Edge: edgeSelection(cfg)}
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

func printReleasePlan(out io.Writer, base string, plan *deploymentsv1.PlanReleaseDomainResponse) {
	header := fmt.Sprintf("This will release %s and stop serving every project's previews on it", wildcardOf(base))
	if kind := plan.GetEdgeStack().GetEdgeKind(); kind != "" {
		header += fmt.Sprintf(", fronted by the %s edge", kind)
	}
	fmt.Fprintf(out, "%s:\n", header)
	kept := printPlanItems(out, plan.GetEdgeStack().GetItems())
	fmt.Fprintln(out, "This cannot be undone.")
	printKeptItems(out, kept)
}

func runAddDomain(ctx context.Context, d deps, cwd, host string, stdout, stderr io.Writer) error {
	cfg, provider, err := domainSession(ctx, cwd)
	if err != nil {
		return err
	}
	configured := declaredHostnames(cfg, "production")
	if len(configured) == 0 {
		return fmt.Errorf("this project declares no domains.production in %s, so there is no production hostname to add: declare one and run `ocel domain add` again — no command edits the config", filepath.Base(cfg.Path))
	}
	return runDomainStream(ctx, d, cfg, provider, "ocel domain add", stdout, stderr, func(runner *providerrunner.Runner, ui *deployui.Session) error {
		req := &deploymentsv1.AddDomainRequest{
			Slug:       cfg.Slug,
			Configured: configured,
			Host:       host,
			Edge:       edgeSelection(cfg),
		}
		if err := runner.AddDomain(ctx, req, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Serving %s", strings.Join(addedHosts(configured, host), ", ")))
		return nil
	})
}

func addedHosts(configured []string, host string) []string {
	if host == "" {
		return configured
	}
	return []string{host}
}

func runDomainRm(ctx context.Context, d deps, cwd, host string, stdout, stderr io.Writer) error {
	cfg, provider, err := domainSession(ctx, cwd)
	if err != nil {
		return err
	}

	return runDomainStream(ctx, d, cfg, provider, "ocel domain rm", stdout, stderr, func(runner *providerrunner.Runner, ui *deployui.Session) error {
		req := &deploymentsv1.RemoveDomainRequest{
			Slug:       cfg.Slug,
			Configured: declaredHostnames(cfg, "production"),
			Host:       host,
			Edge:       edgeSelection(cfg),
		}
		if err := runner.RemoveDomain(ctx, req, ui.Event); err != nil {
			return err
		}
		if host != "" {
			ui.Finish(fmt.Sprintf("Removed %s", host))
			return nil
		}
		ui.Finish("Removed every hostname this project no longer declares")
		return nil
	})
}

func runDomainStream(ctx context.Context, d deps, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor, command string, stdout, stderr io.Writer, act func(*providerrunner.Runner, *deployui.Session) error) error {
	ctx, run, err := startRun(ctx, cfg, command)
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sessionFormat(), verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, d, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		if err := preflightClass(ctx, d, runner, cfg, deploymentsv1.Environment_CLASS_PRODUCTION, "ocel bootstrap", stdout); err != nil {
			return err
		}
		return act(runner, ui)
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

func domainSession(ctx context.Context, cwd string) (*projectconfig.Config, *projectconfig.ProviderDescriptor, error) {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
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

func listGlobalPreviewDomain(ctx context.Context, d deps, runner *providerrunner.Runner, cfg *projectconfig.Config, out io.Writer) (*deploymentsv1.ListDomainResponse, error) {
	if err := preflightPreview(ctx, d, runner, cfg, out); err != nil {
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
	if scope := domain.GetEdgeScope(); scope != "" {
		fmt.Fprintf(out, "  Edge account         %s\n", scope)
	}
	fmt.Fprintf(out, "  Hostname grammar     %d–%d\n", domain.GetGrammarMin(), domain.GetGrammarMax())
	route := "installed"
	if !domain.GetRouteInstalled() {
		route = "MISSING — run `ocel domain use '" + wildcardOf(domain.GetBaseDomain()) + "' --preview` to reinstall it"
	}
	fmt.Fprintf(out, "  Wildcard route       %s\n", route)
	if id := domain.GetCertificateId(); id != "" {
		fmt.Fprintf(out, "  Certificate          %s  %s\n", domain.GetCertificateStatus(), id)
	}
	renderDomainRecords(out, "Records ocel wrote", domain.GetRecordsWritten(), "none — nothing here writes DNS")
	renderDomainRecords(out, "Records you own", domain.GetRecordsOwed(), "none outstanding")
	fmt.Fprintf(out, "  Last probe           %s\n", lastProbe(domain))
	renderGlobalDomainProjects(out, resp.GetProjects())
}

func renderDomainRecords(out io.Writer, name string, records []string, empty string) {
	if len(records) == 0 {
		fmt.Fprintf(out, "  %-20s %s\n", name, empty)
		return
	}
	for i, rec := range records {
		fmt.Fprintf(out, "  %-20s %s\n", label(i, name), rec)
	}
}

func label(i int, name string) string {
	if i == 0 {
		return name
	}
	return ""
}

func lastProbe(domain *deploymentsv1.GlobalPreviewDomain) string {
	if domain.GetLastProbeAt() == 0 {
		return "never — run `ocel domain use '" + wildcardOf(domain.GetBaseDomain()) + "' --preview` to check the edge answers"
	}
	at := time.Unix(domain.GetLastProbeAt(), 0).UTC().Format(time.RFC3339)
	if !domain.GetLastProbeOk() {
		return fmt.Sprintf("%s  FAILED — nothing answered as the %s edge", at, domain.GetLastProbeEdge())
	}
	return fmt.Sprintf("%s  x-ocel-edge: %s", at, domain.GetLastProbeEdge())
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

func wildcardOf(base string) string {
	return "*." + base
}

type domainWaitSchedule struct {
	first, most, cap time.Duration
}

var domainWait = domainWaitSchedule{first: 2 * time.Second, most: 30 * time.Second, cap: 15 * time.Minute}

func runDomainStatus(ctx context.Context, d deps, cwd string, opts domainOptions, stdout, stderr io.Writer) error {
	cfg, provider, err := domainSession(ctx, cwd)
	if err != nil {
		return err
	}
	configured := declaredHostnames(cfg, "production")

	return runProviderSession(ctx, d, cfg, provider, stdout, stderr, func(runner *providerrunner.Runner) error {
		if err := preflightClass(ctx, d, runner, cfg, deploymentsv1.Environment_CLASS_PRODUCTION, "ocel bootstrap", stdout); err != nil {
			return err
		}
		client, err := runner.Deployments()
		if err != nil {
			return err
		}
		req := &deploymentsv1.DomainStatusRequest{
			Slug:       cfg.Slug,
			Configured: configured,
			Edge:       edgeSelection(cfg),
		}
		resp, err := awaitDomainStatus(ctx, client, req, opts.wait, stdout)
		if err != nil {
			return err
		}
		if logFormat() == logFormatJSON {
			return writeDomainStatusJSON(stdout, resp)
		}
		renderDomainStatus(stdout, resp, filepath.Base(cfg.Path))
		return nil
	})
}

const domainWaitFailures = 4

func awaitDomainStatus(ctx context.Context, client deploymentsv1connect.ProviderServiceClient, req *deploymentsv1.DomainStatusRequest, wait bool, out io.Writer) (*deploymentsv1.DomainStatusResponse, error) {
	resp, err := client.DomainStatus(ctx, req)
	if err != nil || !wait || resp.GetReady() {
		return resp, err
	}
	if declaredHosts(resp) == 0 {
		return resp, fmt.Errorf("this project declares no production hostname, so there is nothing to wait for: declare one under domains.production and run `ocel domain add`")
	}

	spinner := deployui.StartSpinner(out, "Waiting for every declared hostname to answer")
	defer spinner.Stop()

	giveUp := time.Now().Add(domainWait.cap)
	every := domainWait.first
	var failures int
	var lastErr error
	for {
		if err := holdFor(ctx, jittered(every)); err != nil {
			return nil, err
		}
		every = min(every*2, domainWait.most)
		next, err := client.DomainStatus(ctx, req)
		switch {
		case err != nil:
			failures, lastErr = failures+1, err
			if failures >= domainWaitFailures {
				return resp, fmt.Errorf("gave up after %d failed checks in a row; the last one said: %w", failures, err)
			}
		default:
			failures, lastErr, resp = 0, nil, next
			if resp.GetReady() {
				return resp, nil
			}
		}
		if time.Now().After(giveUp) {
			if lastErr != nil {
				return resp, fmt.Errorf("gave up after %s waiting for every production hostname to answer; the last check failed: %w", domainWait.cap, lastErr)
			}
			return resp, fmt.Errorf("gave up after %s waiting for every production hostname to answer; still outstanding: %s", domainWait.cap, outstandingHosts(resp))
		}
	}
}

func declaredHosts(resp *deploymentsv1.DomainStatusResponse) int {
	var declared int
	for _, host := range resp.GetHosts() {
		if host.GetDeclared() {
			declared++
		}
	}
	return declared
}

func jittered(every time.Duration) time.Duration {
	return every + time.Duration(rand.Float64()*float64(every)/4)
}

func holdFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func outstandingHosts(resp *deploymentsv1.DomainStatusResponse) string {
	var pending []string
	for _, host := range resp.GetHosts() {
		if !host.GetReady() {
			pending = append(pending, host.GetPending())
		}
	}
	if len(pending) == 0 {
		return "nothing this project declares"
	}
	return strings.Join(pending, "; ")
}

type domainStatusReport struct {
	Ready          bool               `json:"ready"`
	RecordsWritten []string           `json:"recordsWritten,omitempty"`
	RecordsOwed    []string           `json:"recordsOwed,omitempty"`
	Hosts          []domainHostReport `json:"hosts"`
}

type domainHostReport struct {
	Hostname       string   `json:"hostname"`
	Declared       bool     `json:"declared"`
	Ready          bool     `json:"ready"`
	Pending        string   `json:"pending,omitempty"`
	Certificate    string   `json:"certificate,omitempty"`
	CertStatus     string   `json:"certificateStatus,omitempty"`
	Renewal        string   `json:"renewal,omitempty"`
	ExpiresAt      string   `json:"expiresAt,omitempty"`
	ExpiringSoon   bool     `json:"expiringSoon,omitempty"`
	RecordsWritten []string `json:"recordsWritten,omitempty"`
	RecordsOwed    []string `json:"recordsOwed,omitempty"`
	LastProbeAt    string   `json:"lastProbeAt,omitempty"`
	LastProbeOk    bool     `json:"lastProbeOk"`
	LastProbeEdge  string   `json:"lastProbeEdge,omitempty"`
	ServingPointer string   `json:"servingPointer,omitempty"`
}

func writeDomainStatusJSON(out io.Writer, resp *deploymentsv1.DomainStatusResponse) error {
	report := domainStatusReport{
		Ready:          resp.GetReady(),
		RecordsWritten: resp.GetRecordsWritten(),
		RecordsOwed:    resp.GetRecordsOwed(),
		Hosts:          make([]domainHostReport, 0, len(resp.GetHosts())),
	}
	for _, host := range resp.GetHosts() {
		report.Hosts = append(report.Hosts, domainHostReport{
			Hostname:       host.GetHostname(),
			Declared:       host.GetDeclared(),
			Ready:          host.GetReady(),
			Pending:        host.GetPending(),
			Certificate:    host.GetCertificateId(),
			CertStatus:     host.GetCertificateStatus(),
			Renewal:        host.GetRenewalStatus(),
			ExpiresAt:      stamp(host.GetExpiresAt()),
			ExpiringSoon:   host.GetExpiringSoon(),
			RecordsWritten: host.GetRecordsWritten(),
			RecordsOwed:    host.GetRecordsOwed(),
			LastProbeAt:    stamp(host.GetLastProbeAt()),
			LastProbeOk:    host.GetLastProbeOk(),
			LastProbeEdge:  host.GetLastProbeEdge(),
			ServingPointer: host.GetServingPointer(),
		})
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func stamp(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func renderDomainStatus(out io.Writer, resp *deploymentsv1.DomainStatusResponse, configName string) {
	if len(resp.GetHosts()) == 0 {
		fmt.Fprintf(out, "This project declares no domains.production in %s, so nothing is served under a hostname of its own.\n", configName)
		return
	}
	if len(resp.GetRecordsWritten()) > 0 || len(resp.GetRecordsOwed()) > 0 {
		fmt.Fprintln(out, "Certificate validation")
		renderDomainRecords(out, "Records ocel wrote", resp.GetRecordsWritten(), "none — nothing here writes DNS")
		renderDomainRecords(out, "Records you own", resp.GetRecordsOwed(), "none outstanding")
		fmt.Fprintln(out)
	}
	for i, host := range resp.GetHosts() {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%s  %s\n", host.GetHostname(), domainHostState(host))
		if !host.GetDeclared() {
			fmt.Fprintf(out, "  %-20s no — %s no longer declares it\n", "Declared", configName)
		}
		if id := host.GetCertificateId(); id != "" {
			fmt.Fprintf(out, "  %-20s %s  %s\n", "Certificate", host.GetCertificateStatus(), id)
			fmt.Fprintf(out, "  %-20s %s\n", "Renewal", domainRenewal(host))
		}
		renderDomainRecords(out, "Records ocel wrote", host.GetRecordsWritten(), "none — nothing here writes DNS")
		renderDomainRecords(out, "Records you own", host.GetRecordsOwed(), "none outstanding")
		fmt.Fprintf(out, "  %-20s %s\n", "Last probe", domainHostProbe(host))
		if pointer := host.GetServingPointer(); pointer != "" {
			fmt.Fprintf(out, "  %-20s %s\n", "Served by", pointer)
		}
		if pending := host.GetPending(); pending != "" {
			fmt.Fprintf(out, "  %-20s %s\n", "Outstanding", pending)
		}
	}
}

func domainHostState(host *deploymentsv1.DomainHost) string {
	if host.GetReady() {
		return "READY"
	}
	return "PENDING"
}

func domainRenewal(host *deploymentsv1.DomainHost) string {
	expiry := "no expiry reported"
	if at := host.GetExpiresAt(); at != 0 {
		expiry = "expires " + stamp(at)
	}
	status := host.GetRenewalStatus()
	if status == "" {
		status = "not reported"
	}
	if host.GetExpiringSoon() {
		return fmt.Sprintf("%s, %s — EXPIRING SOON", expiry, status)
	}
	return fmt.Sprintf("%s, %s", expiry, status)
}

func domainHostProbe(host *deploymentsv1.DomainHost) string {
	if host.GetLastProbeAt() == 0 {
		return "never — run `ocel domain add` to bind it and check the edge answers"
	}
	at := stamp(host.GetLastProbeAt())
	if !host.GetLastProbeOk() {
		return fmt.Sprintf("%s  FAILED — nothing answered as the %s edge", at, host.GetLastProbeEdge())
	}
	return fmt.Sprintf("%s  x-ocel-edge: %s", at, host.GetLastProbeEdge())
}
