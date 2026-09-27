package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/preflight"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/runui"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
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
	Long: "Manage this project's production hostnames, and the bootstrap-wide domain every project's previews are served on.\n\n" +
		"`add`, `rm`, `ls` and `status` are project-scoped and read domains.production, which is the declaration: " +
		"no command edits it. `use` and `release` take --preview and act on the bootstrap, where " +
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
		return runDomainUse(ctx, newDeps(), cwd, args[0], domainOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var domainLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List this project's production hostnames, or with --preview the global domain and the projects served on it",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()
		return runDomainLs(ctx, newDeps(), cwd, domainOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
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
		return runDomainRelease(ctx, newDeps(), cwd, domainOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
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
		return runDomainAdd(ctx, newDeps(), cwd, firstArg(args), cmd.OutOrStdout(), cmd.ErrOrStderr())
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
		return runDomainRm(ctx, newDeps(), cwd, firstArg(args), cmd.OutOrStdout(), cmd.ErrOrStderr())
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
		return runDomainStatus(ctx, newDeps(), cwd, domainOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func init() {
	for _, c := range []*cobra.Command{domainUseCmd, domainReleaseCmd} {
		c.Flags().BoolVar(&domainOpts.preview, "preview", false, "Act on the preview class (required)")
	}
	domainLsCmd.Flags().BoolVar(&domainOpts.preview, "preview", false, "List the global preview domain and the projects served on it instead of this project's own hostnames")
	cmddeps.Yes(domainReleaseCmd, &domainOpts.yes)
	domainStatusCmd.Flags().BoolVar(&domainOpts.wait, "wait", false, "Keep polling until every declared hostname is served, or give up")

	domainCmd.AddCommand(cmddeps.ReserveStdout(domainStatusCmd))
	domainCmd.AddCommand(domainAddCmd)
	domainCmd.AddCommand(domainRmCmd)
	domainCmd.AddCommand(domainUseCmd)
	domainCmd.AddCommand(cmddeps.ReserveStdout(domainLsCmd))
	domainCmd.AddCommand(domainReleaseCmd)
	rootCmd.AddCommand(domainCmd)
}

func requirePreviewClass(command string, preview bool) error {
	if preview {
		return nil
	}
	return fmt.Errorf("`%s` needs --preview: a global domain is preview-only — a production hostname belongs to one project and is declared in that project's %s, so there is no global production domain to manage",
		command, projectconfig.DefaultFileName)
}

func globalPreviewBaseDomain(wildcard string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(wildcard))
	if err := projectconfig.ValidatePreviewDomain(host); err != nil {
		return "", err
	}
	return projectconfig.PreviewBaseDomain(host), nil
}

func runDomainUse(ctx context.Context, deps cmddeps.Deps, cwd, wildcard string, opts domainOptions, stdout, stderr io.Writer) (err error) {
	if err := requirePreviewClass("ocel domain use", opts.preview); err != nil {
		return err
	}
	base, err := globalPreviewBaseDomain(wildcard)
	if err != nil {
		return err
	}

	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}
	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}

	ctx, run, err := deps.Events.Begin(ctx, "ocel domain use", cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := startReadyProvider(ctx, deps, cfg, check, environmentv1.Tier_TIER_PREVIEW)
	check.End(err)
	if err != nil {
		return err
	}
	defer prov.Close()

	req := &contractv1.UsePreviewWildcardRequest{
		Tier:       environmentv1.Tier_TIER_PREVIEW,
		BaseDomain: base,
		Edge:       edgewire.Selection(cfg),
	}
	if _, err := providerclient.Stream(ctx, prov, "UsePreviewWildcard", req, contractv1connect.ProviderServiceClient.UsePreviewWildcard); err != nil {
		return err
	}
	run.Finish(fmt.Sprintf("Previews are served on %s", wildcardOf(base)))
	return nil
}

func startReadyProvider(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, check *events.Scope, tier environmentv1.Tier) (*providerclient.Provider, error) {
	prov, err := providerclient.Start(ctx, cfg, check, deps.HostTrust, providerclient.PinToLock)
	if err != nil {
		return nil, err
	}
	if err := bootstrap.Ready(ctx, check, prov, cfg, tier, "ocel bootstrap "+bootstrap.Name(tier)); err != nil {
		prov.Close()
		return nil, err
	}
	return prov, nil
}

func runDomainLs(ctx context.Context, deps cmddeps.Deps, cwd string, opts domainOptions, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	if !opts.preview {
		resp, err := listProductionHostnames(ctx, deps, cfg)
		if err != nil {
			return err
		}
		renderBoundHostnames(stdout, resp, filepath.Base(cfg.Path))
		return nil
	}
	resp, err := listGlobalPreviewDomain(ctx, deps, cfg)
	if err != nil {
		return err
	}
	renderGlobalDomain(stdout, resp)
	return nil
}

func listProductionHostnames(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config) (resp *contractv1.GetHostnameStatusResponse, err error) {
	err = readDomain(ctx, deps, cfg, "ocel domain ls", environmentv1.Tier_TIER_PRODUCTION, "Reading the hostnames this project serves",
		func(ctx context.Context, client contractv1connect.ProviderServiceClient) (err error) {
			resp, err = client.GetHostnameStatus(ctx, &contractv1.HostnameRequest{
				Slug:       cfg.Slug,
				Configured: preflight.Configured(preflight.Hostnames(cfg, "production")),
				Edge:       edgewire.Selection(cfg),
			})
			return err
		})
	return resp, err
}

func readDomain(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, command string, tier environmentv1.Tier, reading string, read func(context.Context, contractv1connect.ProviderServiceClient) error) (err error) {
	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}

	ctx, run, err := deps.Events.Begin(ctx, command, cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := startReadyProvider(ctx, deps, cfg, check, tier)
	if err != nil {
		return err
	}
	defer prov.Close()

	unit := check.Unit(cfg.Slug, reading)
	err = prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) error { return read(ctx, client) })
	unit.End(err)
	check.End(err)
	return err
}

func renderBoundHostnames(out io.Writer, resp *contractv1.GetHostnameStatusResponse, configName string) {
	if len(resp.GetHostnames()) == 0 {
		fmt.Fprintf(out, "This project declares no domains.production in %s and serves none.\n", configName)
		fmt.Fprintln(out, "  → declare one and run `ocel domain add`; `ocel domain ls --preview` shows the domain every project's previews share")
		return
	}
	for _, host := range resp.GetHostnames() {
		fmt.Fprintf(out, "%-8s %s", domainHostState(host), host.GetHostname())
		if pointer := host.GetServingPointer(); pointer != "" {
			fmt.Fprintf(out, "  → %s", pointer)
		}
		fmt.Fprintln(out)
		if pending := host.GetPending(); pending != "" {
			fmt.Fprintf(out, "         %s\n", pending)
		}
	}
}

func runDomainRelease(ctx context.Context, deps cmddeps.Deps, cwd string, opts domainOptions, stdout, stderr io.Writer, stdin io.Reader) (err error) {
	if err := requirePreviewClass("ocel domain release", opts.preview); err != nil {
		return err
	}
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}
	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}

	gate := deps.Gate(consent.PlanFirst, "ocel domain release", opts.yes, stdout, stdin)
	gate.Unattended = "pass --yes"
	if err := gate.Refuse(); err != nil {
		return err
	}

	ctx, run, err := deps.Events.Begin(ctx, gate.Command, cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := startReadyProvider(ctx, deps, cfg, check, environmentv1.Tier_TIER_PREVIEW)
	check.End(err)
	if err != nil {
		return err
	}
	defer prov.Close()

	planning := run.Phase(progressv1.Phase_PHASE_PLAN)
	unit := planning.Unit(cfg.Slug, "Enumerating what releasing the domain would remove")
	var plan *planv1.ChangePlan
	err = prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		plan, err = client.PlanRemovePreviewWildcard(ctx, &contractv1.PreviewWildcardRequest{
			Tier: environmentv1.Tier_TIER_PREVIEW,
		})
		return err
	})
	unit.End(err)
	if err != nil {
		return err
	}
	base := plan.GetSubject()
	if base == "" {
		run.Finish("No global preview domain is configured")
		return nil
	}

	shown := planning.Plan(fmt.Sprintf("This will release %s and stop serving every project's previews on it", wildcardOf(base)), plan,
		"This cannot be undone.")
	granted, err := gate.ConsentByName(ctx, planning, shown, "domain", base)
	planning.End(err)
	if err != nil {
		return err
	}
	if !granted {
		run.Finish("Nothing released")
		return nil
	}

	req := &contractv1.PreviewWildcardRequest{Tier: environmentv1.Tier_TIER_PREVIEW, Edge: edgewire.Selection(cfg)}
	if _, err := providerclient.Stream(ctx, prov, "RemovePreviewWildcard", req, contractv1connect.ProviderServiceClient.RemovePreviewWildcard); err != nil {
		return err
	}
	run.Finish(fmt.Sprintf("Released %s", wildcardOf(base)))
	return nil
}

func runDomainAdd(ctx context.Context, deps cmddeps.Deps, cwd, host string, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}
	declared := preflight.Hostnames(cfg, "production")
	configured := preflight.Names(declared)
	if len(configured) == 0 {
		return fmt.Errorf("this project declares no domains.production in %s, so there is no production hostname to add: declare one and run `ocel domain add` again — no command edits the config", filepath.Base(cfg.Path))
	}
	req := &contractv1.HostnameRequest{
		Slug:       cfg.Slug,
		Configured: preflight.Configured(declared),
		Host:       host,
		Edge:       edgewire.Selection(cfg),
	}
	return changeHostnames(ctx, deps, cfg, "ocel domain add", "AddHostname", req, contractv1connect.ProviderServiceClient.AddHostname,
		fmt.Sprintf("Serving %s", strings.Join(addedHosts(configured, host), ", ")))
}

func changeHostnames(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, command, rpc string, req *contractv1.HostnameRequest, call func(contractv1connect.ProviderServiceClient, context.Context, *contractv1.HostnameRequest) (*connect.ServerStreamForClient[progressv1.OperationEvent], error), headline string) (err error) {
	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}

	ctx, run, err := deps.Events.Begin(ctx, command, cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := startReadyProvider(ctx, deps, cfg, check, environmentv1.Tier_TIER_PRODUCTION)
	check.End(err)
	if err != nil {
		return err
	}
	defer prov.Close()

	if _, err := providerclient.Stream(ctx, prov, rpc, req, call); err != nil {
		return err
	}
	run.Finish(headline)
	return nil
}

func addedHosts(configured []string, host string) []string {
	if host == "" {
		return configured
	}
	return []string{host}
}

func runDomainRm(ctx context.Context, deps cmddeps.Deps, cwd, host string, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}
	req := &contractv1.HostnameRequest{
		Slug:       cfg.Slug,
		Configured: preflight.Configured(preflight.Hostnames(cfg, "production")),
		Host:       host,
		Edge:       edgewire.Selection(cfg),
	}
	headline := "Removed every hostname this project no longer declares"
	if host != "" {
		headline = fmt.Sprintf("Removed %s", host)
	}
	return changeHostnames(ctx, deps, cfg, "ocel domain rm", "RemoveHostname", req, contractv1connect.ProviderServiceClient.RemoveHostname, headline)
}

func listGlobalPreviewDomain(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config) (resp *contractv1.GetPreviewWildcardResponse, err error) {
	err = readDomain(ctx, deps, cfg, "ocel domain ls", environmentv1.Tier_TIER_PREVIEW, "Reading the global preview domain",
		func(ctx context.Context, client contractv1connect.ProviderServiceClient) (err error) {
			resp, err = client.GetPreviewWildcard(ctx, &contractv1.PreviewWildcardRequest{Tier: environmentv1.Tier_TIER_PREVIEW})
			return err
		})
	return resp, err
}

func renderGlobalDomain(out io.Writer, resp *contractv1.GetPreviewWildcardResponse) {
	domain := resp.GetWildcard()
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
	cert := domain.GetCertificate()
	if id := cert.GetCertificateId(); id != "" {
		fmt.Fprintf(out, "  Certificate          %s  %s\n", cert.GetCertificateStatus(), id)
		fmt.Fprintf(out, "  Renewal              %s\n", wildcardRenewal(domain))
	}
	renderCertificateRecords(out, cert)
	fmt.Fprintf(out, "  Last probe           %s\n", lastProbe(cert, "never — run `ocel domain use '"+wildcardOf(domain.GetBaseDomain())+"' --preview` to check the edge answers"))
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

func lastProbe(cert *contractv1.CertificateState, never string) string {
	if cert.GetLastProbeAt() == 0 {
		return never
	}
	at := epochRFC3339(cert.GetLastProbeAt())
	if !cert.GetLastProbeOk() {
		return fmt.Sprintf("%s  FAILED — nothing answered as the %s edge", at, cert.GetLastProbeEdge())
	}
	return fmt.Sprintf("%s  x-ocel-edge: %s", at, cert.GetLastProbeEdge())
}

func renderCertificateRecords(out io.Writer, cert *contractv1.CertificateState) {
	renderDomainRecords(out, "Records ocel wrote", cert.GetRecordsWritten(), "none — nothing here writes DNS")
	renderDomainRecords(out, "Records you own", cert.GetManualRecords(), "none outstanding")
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
	initialInterval, maxInterval, deadline time.Duration
}

var domainWait = domainWaitSchedule{initialInterval: 2 * time.Second, maxInterval: 30 * time.Second, deadline: 15 * time.Minute}

func runDomainStatus(ctx context.Context, deps cmddeps.Deps, cwd string, opts domainOptions, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}
	resp, err := readDomainStatus(ctx, deps, cfg, opts.wait)
	if err != nil {
		return err
	}
	if deps.Presentation(stdout).Format == runui.FormatJSON {
		return writeDomainStatusJSON(stdout, resp)
	}
	renderDomainStatus(stdout, resp, filepath.Base(cfg.Path))
	return nil
}

func readDomainStatus(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, wait bool) (resp *contractv1.GetHostnameStatusResponse, err error) {
	if _, err := cfg.RequireProvider(); err != nil {
		return nil, err
	}

	ctx, run, err := deps.Events.Begin(ctx, "ocel domain status", cfg.Dir)
	if err != nil {
		return nil, err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := startReadyProvider(ctx, deps, cfg, check, environmentv1.Tier_TIER_PRODUCTION)
	if err != nil {
		return nil, err
	}
	defer prov.Close()

	req := &contractv1.HostnameRequest{
		Slug:       cfg.Slug,
		Configured: preflight.Configured(preflight.Hostnames(cfg, "production")),
		Edge:       edgewire.Selection(cfg),
		Probe:      true,
	}
	resp, err = awaitDomainStatus(ctx, check, cfg.Slug, hostnameStatus(prov, req), wait)
	check.End(err)
	return resp, err
}

func hostnameStatus(prov *providerclient.Provider, req *contractv1.HostnameRequest) func(context.Context) (*contractv1.GetHostnameStatusResponse, error) {
	return func(ctx context.Context) (resp *contractv1.GetHostnameStatusResponse, err error) {
		err = prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
			resp, err = client.GetHostnameStatus(ctx, req)
			return err
		})
		return resp, err
	}
}

const domainWaitFailures = 4

func awaitDomainStatus(ctx context.Context, check *events.Scope, slug string, read func(context.Context) (*contractv1.GetHostnameStatusResponse, error), wait bool) (*contractv1.GetHostnameStatusResponse, error) {
	resp, err := read(ctx)
	if err != nil || !wait || resp.GetReady() {
		return resp, err
	}
	if declaredHosts(resp) == 0 {
		return resp, fmt.Errorf("this project declares no production hostname, so there is nothing to wait for: declare one under domains.production and run `ocel domain add`")
	}

	unit := check.Unit(slug, "Waiting for every declared hostname to answer")
	resp, err = pollDomainStatus(ctx, read, resp)
	unit.End(err)
	return resp, err
}

func pollDomainStatus(ctx context.Context, read func(context.Context) (*contractv1.GetHostnameStatusResponse, error), resp *contractv1.GetHostnameStatusResponse) (*contractv1.GetHostnameStatusResponse, error) {
	giveUp := time.Now().Add(domainWait.deadline)
	every := domainWait.initialInterval
	var failures int
	var lastErr error
	for {
		if err := sleepOrCancel(ctx, jittered(every)); err != nil {
			return nil, err
		}
		every = min(every*2, domainWait.maxInterval)
		next, err := read(ctx)
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
				return resp, fmt.Errorf("gave up after %s waiting for every production hostname to answer; the last check failed: %w", domainWait.deadline, lastErr)
			}
			return resp, fmt.Errorf("gave up after %s waiting for every production hostname to answer; still outstanding: %s", domainWait.deadline, outstandingHosts(resp))
		}
	}
}

func declaredHosts(resp *contractv1.GetHostnameStatusResponse) int {
	var declared int
	for _, host := range resp.GetHostnames() {
		if host.GetDeclared() {
			declared++
		}
	}
	return declared
}

func jittered(every time.Duration) time.Duration {
	return every + time.Duration(rand.Float64()*float64(every)/4)
}

func sleepOrCancel(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func outstandingHosts(resp *contractv1.GetHostnameStatusResponse) string {
	var pending []string
	for _, host := range resp.GetHostnames() {
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
	ManualRecords  []string           `json:"manualRecords,omitempty"`
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
	ManualRecords  []string `json:"manualRecords,omitempty"`
	LastProbeAt    string   `json:"lastProbeAt,omitempty"`
	LastProbeOk    bool     `json:"lastProbeOk"`
	LastProbeEdge  string   `json:"lastProbeEdge,omitempty"`
	ServingPointer string   `json:"servingPointer,omitempty"`
}

func writeDomainStatusJSON(out io.Writer, resp *contractv1.GetHostnameStatusResponse) error {
	report := domainStatusReport{
		Ready:          resp.GetReady(),
		RecordsWritten: resp.GetRecordsWritten(),
		ManualRecords:  resp.GetManualRecords(),
		Hosts:          make([]domainHostReport, 0, len(resp.GetHostnames())),
	}
	for _, host := range resp.GetHostnames() {
		cert := host.GetCertificate()
		report.Hosts = append(report.Hosts, domainHostReport{
			Hostname:       host.GetHostname(),
			Declared:       host.GetDeclared(),
			Ready:          host.GetReady(),
			Pending:        host.GetPending(),
			Certificate:    cert.GetCertificateId(),
			CertStatus:     cert.GetCertificateStatus(),
			Renewal:        host.GetRenewalStatus(),
			ExpiresAt:      epochRFC3339(host.GetExpiresAt()),
			ExpiringSoon:   host.GetExpiringSoon(),
			RecordsWritten: cert.GetRecordsWritten(),
			ManualRecords:  cert.GetManualRecords(),
			LastProbeAt:    epochRFC3339(cert.GetLastProbeAt()),
			LastProbeOk:    cert.GetLastProbeOk(),
			LastProbeEdge:  cert.GetLastProbeEdge(),
			ServingPointer: host.GetServingPointer(),
		})
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func epochRFC3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func renderDomainStatus(out io.Writer, resp *contractv1.GetHostnameStatusResponse, configName string) {
	if len(resp.GetHostnames()) == 0 {
		fmt.Fprintf(out, "This project declares no domains.production in %s, so nothing is served under a hostname of its own.\n", configName)
		return
	}
	if len(resp.GetRecordsWritten()) > 0 || len(resp.GetManualRecords()) > 0 {
		fmt.Fprintln(out, "Certificate validation")
		renderDomainRecords(out, "Records ocel wrote", resp.GetRecordsWritten(), "none — nothing here writes DNS")
		renderDomainRecords(out, "Records you own", resp.GetManualRecords(), "none outstanding")
		fmt.Fprintln(out)
	}
	for i, host := range resp.GetHostnames() {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%s  %s\n", host.GetHostname(), domainHostState(host))
		if !host.GetDeclared() {
			fmt.Fprintf(out, "  %-20s no — %s no longer declares it\n", "Declared", configName)
		}
		cert := host.GetCertificate()
		if id := cert.GetCertificateId(); id != "" {
			fmt.Fprintf(out, "  %-20s %s  %s\n", "Certificate", cert.GetCertificateStatus(), id)
			fmt.Fprintf(out, "  %-20s %s\n", "Renewal", domainRenewal(host))
		}
		renderCertificateRecords(out, cert)
		fmt.Fprintf(out, "  %-20s %s\n", "Last probe", lastProbe(cert, "never — run `ocel domain add` to bind it and check the edge answers"))
		if pointer := host.GetServingPointer(); pointer != "" {
			fmt.Fprintf(out, "  %-20s %s\n", "Served by", pointer)
		}
		if pending := host.GetPending(); pending != "" {
			fmt.Fprintf(out, "  %-20s %s\n", "Outstanding", pending)
		}
	}
}

func domainHostState(host *contractv1.ProductionHostname) string {
	if host.GetReady() {
		return "READY"
	}
	return "PENDING"
}

func wildcardRenewal(domain *contractv1.PreviewWildcard) string {
	return renewalLine(domain.GetRenewalStatus(), domain.GetExpiresAt(), domain.GetExpiringSoon())
}

func domainRenewal(host *contractv1.ProductionHostname) string {
	return renewalLine(host.GetRenewalStatus(), host.GetExpiresAt(), host.GetExpiringSoon())
}

func renewalLine(status string, expiresAt int64, soon bool) string {
	expiry := "no expiry reported"
	if expiresAt != 0 {
		expiry = "expires " + epochRFC3339(expiresAt)
	}
	if status == "" {
		status = "not reported"
	}
	if soon {
		return fmt.Sprintf("%s, %s — EXPIRING SOON", expiry, status)
	}
	return fmt.Sprintf("%s, %s", expiry, status)
}
