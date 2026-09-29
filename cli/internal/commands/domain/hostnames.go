package domain

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/preflight"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func runDomainList(ctx context.Context, deps cmddeps.Deps, cwd string, opts domainOptions, stdout, stderr io.Writer) error {
	cfg, err := deps.LoadProject(ctx, cwd)
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

func listProductionHostnames(ctx context.Context, deps cmddeps.Deps, cfg *project.Project) (resp *contractv1.GetHostnameStatusResponse, err error) {
	err = readDomain(ctx, deps, cfg, "ocel domain ls", environmentv1.Tier_TIER_PRODUCTION, progress.Reading.Title("the hostnames this project serves"),
		func(ctx context.Context, client contractv1connect.ProviderServiceClient) (err error) {
			resp, err = client.GetHostnameStatus(ctx, &contractv1.HostnameRequest{
				Slug:       cfg.Slug,
				Configured: preflight.Configured(preflight.Hostnames(cfg, environmentv1.Tier_TIER_PRODUCTION)),
				Edge:       cfg.EdgeSelection(),
			})
			return err
		})
	return resp, err
}

func runDomainAdd(ctx context.Context, deps cmddeps.Deps, cwd, host string, stdout, stderr io.Writer) error {
	cfg, err := deps.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	declared := preflight.Hostnames(cfg, environmentv1.Tier_TIER_PRODUCTION)
	configured := preflight.Names(declared)
	if len(configured) == 0 {
		return fmt.Errorf("this project declares no domains.production in %s, so there is no production hostname to add: declare one and run `ocel domain add` again — no command edits the config", filepath.Base(cfg.Path))
	}
	req := &contractv1.HostnameRequest{
		Slug:       cfg.Slug,
		Configured: preflight.Configured(declared),
		Host:       host,
		Edge:       cfg.EdgeSelection(),
	}
	return changeHostnames(ctx, deps, cfg, hostnameChange{
		command:  "ocel domain add",
		rpc:      "AddHostname",
		req:      req,
		call:     contractv1connect.ProviderServiceClient.AddHostname,
		headline: fmt.Sprintf("Serving %s", strings.Join(addedHosts(configured, host), ", ")),
	})
}

type hostnameChange struct {
	command  string
	rpc      string
	req      *contractv1.HostnameRequest
	call     func(contractv1connect.ProviderServiceClient, context.Context, *contractv1.HostnameRequest) (*connect.ServerStreamForClient[progressv1.OperationEvent], error)
	headline string
	asks     *hostnameConsent
}

type hostnameConsent struct {
	policy   consent.Policy
	plan     string
	question string
	declined string
}

func changeHostnames(ctx context.Context, deps cmddeps.Deps, cfg *project.Project, change hostnameChange) (err error) {
	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}
	if change.asks != nil {
		if err := change.asks.policy.Refuse(); err != nil {
			return err
		}
	}

	ctx, run, err := deps.Events.Begin(ctx, change.command, cfg.Dir)
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

	if change.asks != nil {
		planning := run.Phase(progressv1.Phase_PHASE_PLAN)
		planning.Say(change.asks.plan)
		granted, err := change.asks.policy.ConfirmPlan(ctx, planning, nil, change.asks.question)
		planning.End(err)
		if err != nil {
			return err
		}
		if !granted {
			run.Succeed(change.asks.declined)
			return nil
		}
	}

	if _, err := providerclient.Stream(ctx, prov, change.rpc, change.req, change.call); err != nil {
		return err
	}
	run.Succeed(change.headline)
	return nil
}

func addedHosts(configured []string, host string) []string {
	if host == "" {
		return configured
	}
	return []string{host}
}

func runDomainRemove(ctx context.Context, deps cmddeps.Deps, cwd, host string, opts domainOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := deps.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	req := &contractv1.HostnameRequest{
		Slug:       cfg.Slug,
		Configured: preflight.Configured(preflight.Hostnames(cfg, environmentv1.Tier_TIER_PRODUCTION)),
		Host:       host,
		Edge:       cfg.EdgeSelection(),
	}
	headline := "Removed every hostname this project no longer declares"
	plan := fmt.Sprintf("This will unbind every production hostname project %q no longer declares, and remove the certificates and DNS records ocel created for them", cfg.Slug)
	if host != "" {
		headline = fmt.Sprintf("Removed %s", host)
		plan = fmt.Sprintf("This will unbind %s from production of project %q, and remove the certificate and DNS records ocel created for it", host, cfg.Slug)
	}
	policy := deps.ConsentPolicy("ocel domain rm", opts.yes, stdout, stdin)
	policy.ConfirmsPlan = true
	return changeHostnames(ctx, deps, cfg, hostnameChange{
		command:  "ocel domain rm",
		rpc:      "RemoveHostname",
		req:      req,
		call:     contractv1connect.ProviderServiceClient.RemoveHostname,
		headline: headline,
		asks: &hostnameConsent{
			policy:   policy,
			plan:     plan,
			question: "Remove them?",
			declined: "Nothing removed: every production hostname stays as it is",
		},
	})
}

func listGlobalPreviewDomain(ctx context.Context, deps cmddeps.Deps, cfg *project.Project) (resp *contractv1.GetPreviewWildcardResponse, err error) {
	err = readDomain(ctx, deps, cfg, "ocel domain ls", environmentv1.Tier_TIER_PREVIEW, progress.Reading.Title("the global preview domain"),
		func(ctx context.Context, client contractv1connect.ProviderServiceClient) (err error) {
			resp, err = client.GetPreviewWildcard(ctx, &contractv1.PreviewWildcardRequest{Tier: environmentv1.Tier_TIER_PREVIEW})
			return err
		})
	return resp, err
}

func newListCommand(deps cmddeps.Deps) *cobra.Command {
	var opts domainOptions
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List this project's production hostnames, or with --preview the global domain and the projects served on it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return runDomainList(cmd.Context(), deps, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVar(&opts.preview, "preview", false, "List the global preview domain and the projects served on it instead of this project's own hostnames")
	return cmd
}

func newAddCommand(deps cmddeps.Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "add [host]",
		Short: "Provision the certificate, the edge surface and the DNS for this project's production hostnames",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return runDomainAdd(cmd.Context(), deps, cwd, firstArg(args), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func newRemoveCommand(deps cmddeps.Deps) *cobra.Command {
	var opts domainOptions
	cmd := &cobra.Command{
		Use:   "rm [host]",
		Short: "Unbind production hostnames this project no longer declares, and remove what ocel created for them",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return runDomainRemove(cmd.Context(), deps, cwd, firstArg(args), opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}
	cmddeps.Yes(cmd, &opts.yes)
	return cmd
}
