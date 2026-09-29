package domain

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

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type domainWaitSchedule struct {
	initialInterval, maxInterval, deadline time.Duration
}

func runDomainStatus(ctx context.Context, invocation commands.Invocation, cwd string, opts domainOptions, schedule domainWaitSchedule, stdout, stderr io.Writer) error {
	cfg, err := invocation.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	resp, err := readDomainStatus(ctx, invocation, cfg, opts.wait, schedule)
	if err != nil {
		return err
	}
	if invocation.Presentation(stdout).Format == terminal.FormatJSON {
		return writeDomainStatusJSON(stdout, resp)
	}
	renderDomainStatus(stdout, resp, filepath.Base(cfg.Path))
	return nil
}

func readDomainStatus(ctx context.Context, invocation commands.Invocation, cfg *project.Project, wait bool, schedule domainWaitSchedule) (resp *contractv1.GetHostnameStatusResponse, err error) {
	if _, err := cfg.RequireProvider(); err != nil {
		return nil, err
	}

	ctx, run, err := invocation.Events.Begin(ctx, "ocel domain status", cfg.Dir)
	if err != nil {
		return nil, err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	provider, _, err := invocation.OpenProvider(ctx, check, cfg, commands.OpenOptions{Tier: environmentv1.Tier_TIER_PRODUCTION, Require: readiness.Features})
	if err != nil {
		return nil, err
	}
	defer provider.Close()

	req := &contractv1.HostnameRequest{
		Slug:       cfg.Slug,
		Configured: cfg.ConfiguredHostnames(environmentv1.Tier_TIER_PRODUCTION),
		Edge:       cfg.EdgeSelection(),
		Probe:      true,
	}
	resp, err = awaitDomainStatus(ctx, check, cfg.Slug, hostnameStatus(provider, req), wait, schedule)
	check.End(err)
	return resp, err
}

func hostnameStatus(provider *providerprocess.Provider, req *contractv1.HostnameRequest) func(context.Context) (*contractv1.GetHostnameStatusResponse, error) {
	return func(ctx context.Context) (resp *contractv1.GetHostnameStatusResponse, err error) {
		err = provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
			resp, err = client.GetHostnameStatus(ctx, req)
			return err
		})
		return resp, err
	}
}

const domainWaitFailures = 4

func awaitDomainStatus(ctx context.Context, check *run.Span, slug string, read func(context.Context) (*contractv1.GetHostnameStatusResponse, error), wait bool, schedule domainWaitSchedule) (*contractv1.GetHostnameStatusResponse, error) {
	resp, err := read(ctx)
	if err != nil || !wait || resp.GetReady() {
		return resp, err
	}
	if declaredHosts(resp) == 0 {
		return resp, fmt.Errorf("this project declares no production hostname, so there is nothing to wait for: declare one under domains.production and run `ocel domain add`")
	}

	unit := check.Unit(slug, describeWait(declaredHosts(resp)))
	resp, err = pollDomainStatus(ctx, read, resp, schedule)
	unit.End(err)
	return resp, err
}

func pollDomainStatus(ctx context.Context, read func(context.Context) (*contractv1.GetHostnameStatusResponse, error), resp *contractv1.GetHostnameStatusResponse, schedule domainWaitSchedule) (*contractv1.GetHostnameStatusResponse, error) {
	giveUp := time.Now().Add(schedule.deadline)
	every := schedule.initialInterval
	var failures int
	var lastErr error
	for {
		if err := sleepOrCancel(ctx, jittered(every)); err != nil {
			return nil, err
		}
		every = min(every*2, schedule.maxInterval)
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
				return resp, fmt.Errorf("gave up after %s waiting for every production hostname to answer; the last check failed: %w", schedule.deadline, lastErr)
			}
			return resp, fmt.Errorf("gave up after %s waiting for every production hostname to answer; still outstanding: %s", schedule.deadline, outstandingHosts(resp))
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

func describeWait(declared int) progress.Title {
	if declared == 1 {
		return progress.Waiting.Title("for the one declared production hostname to answer")
	}
	return progress.Waiting.Title(fmt.Sprintf("for the %d declared production hostnames to answer", declared))
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

func newStatusCommand(invocation commands.Invocation) *cobra.Command {
	var opts domainOptions
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show, per production hostname, its certificate, the records it needs, what last answered for it and what is still outstanding",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			schedule := domainWaitSchedule{initialInterval: 2 * time.Second, maxInterval: 30 * time.Second, deadline: 15 * time.Minute}
			return runDomainStatus(cmd.Context(), invocation, cwd, opts, schedule, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVar(&opts.wait, "wait", false, "Keep polling until every declared hostname is served, or give up")
	return cmd
}
