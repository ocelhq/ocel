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

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func runDomainList(ctx context.Context, invocation commands.Invocation, cwd string, opts domainOptions, stdout io.Writer) error {
	cfg, err := invocation.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	if !opts.preview {
		resp, err := listProductionHostnames(ctx, invocation, cfg)
		if err != nil {
			return err
		}
		renderBoundHostnames(stdout, resp, filepath.Base(cfg.Path))
		return nil
	}
	resp, err := listGlobalPreviewDomain(ctx, invocation, cfg)
	if err != nil {
		return err
	}
	renderGlobalDomain(stdout, resp)
	return nil
}

func listProductionHostnames(ctx context.Context, invocation commands.Invocation, cfg *project.Project) (resp *contractv1.GetHostnameStatusResponse, err error) {
	err = readDomain(ctx, invocation, cfg, "ocel domain ls", environmentv1.Tier_TIER_PRODUCTION, progress.Reading.Title("the hostnames this project serves"),
		func(ctx context.Context, client contractv1connect.ProviderServiceClient) (err error) {
			resp, err = client.GetHostnameStatus(ctx, &contractv1.HostnameRequest{
				Slug:       cfg.Slug,
				Configured: cfg.ConfiguredHostnames(environmentv1.Tier_TIER_PRODUCTION),
				Edge:       cfg.EdgeSelection(),
			})
			return err
		})
	return resp, err
}

func runDomainAdd(ctx context.Context, invocation commands.Invocation, cwd, host string, stdout io.Writer) error {
	cfg, err := invocation.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	configured := cfg.HostnameNames(environmentv1.Tier_TIER_PRODUCTION)
	if len(configured) == 0 {
		return fmt.Errorf("this project declares no domains.production in %s, so there is no production hostname to add: declare one and run `ocel domain add` again — no command edits the config", filepath.Base(cfg.Path))
	}
	req := &contractv1.HostnameRequest{
		Slug:       cfg.Slug,
		Configured: cfg.ConfiguredHostnames(environmentv1.Tier_TIER_PRODUCTION),
		Host:       host,
		Edge:       cfg.EdgeSelection(),
	}
	return changeHostnames(ctx, invocation, cfg, hostnameChange{
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

func changeHostnames(ctx context.Context, invocation commands.Invocation, cfg *project.Project, change hostnameChange) error {
	if change.asks != nil {
		if err := change.asks.policy.Refuse(); err != nil {
			return err
		}
	}

	return invocation.WithProvider(ctx, cfg, change.command, commands.OpenOptions{Tier: environmentv1.Tier_TIER_PRODUCTION, Require: readiness.Features}, func(ctx context.Context, p commands.ProviderRun) error {
		p.Check.End(nil)
		run, provider := p.Run, p.Provider
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

		if _, err := providerprocess.Stream(ctx, provider, change.rpc, change.req, change.call); err != nil {
			return err
		}
		run.Succeed(change.headline)
		return nil
	})
}

func addedHosts(configured []string, host string) []string {
	if host == "" {
		return configured
	}
	return []string{host}
}

func runDomainRemove(ctx context.Context, invocation commands.Invocation, cwd, host string, opts domainOptions, stdout io.Writer, stdin io.Reader) error {
	cfg, err := invocation.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	req := &contractv1.HostnameRequest{
		Slug:       cfg.Slug,
		Configured: cfg.ConfiguredHostnames(environmentv1.Tier_TIER_PRODUCTION),
		Host:       host,
		Edge:       cfg.EdgeSelection(),
	}
	headline := "Removed every hostname this project no longer declares"
	plan := fmt.Sprintf("This will unbind every production hostname project %q no longer declares, and remove the certificates and DNS records ocel created for them", cfg.Slug)
	if host != "" {
		headline = fmt.Sprintf("Removed %s", host)
		plan = fmt.Sprintf("This will unbind %s from production of project %q, and remove the certificate and DNS records ocel created for it", host, cfg.Slug)
	}
	policy := consent.NewPlanPolicy("ocel domain rm", opts.yes, invocation.CanAsk(stdin), stdout, stdin)
	return changeHostnames(ctx, invocation, cfg, hostnameChange{
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

func listGlobalPreviewDomain(ctx context.Context, invocation commands.Invocation, cfg *project.Project) (resp *contractv1.GetPreviewWildcardResponse, err error) {
	err = readDomain(ctx, invocation, cfg, "ocel domain ls", environmentv1.Tier_TIER_PREVIEW, progress.Reading.Title("the global preview domain"),
		func(ctx context.Context, client contractv1connect.ProviderServiceClient) (err error) {
			resp, err = client.GetPreviewWildcard(ctx, &contractv1.PreviewWildcardRequest{Tier: environmentv1.Tier_TIER_PREVIEW})
			return err
		})
	return resp, err
}

func newListCommand(invocation commands.Invocation) *cobra.Command {
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
			return runDomainList(cmd.Context(), invocation, cwd, opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&opts.preview, "preview", false, "List the global preview domain and the projects served on it instead of this project's own hostnames")
	return commands.DeclareReadOnly(cmd)
}

func newAddCommand(invocation commands.Invocation) *cobra.Command {
	return commands.DeclareMutating(&cobra.Command{
		Use:   "add [host]",
		Short: "Provision the certificate, the edge surface and the DNS for this project's production hostnames",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return runDomainAdd(cmd.Context(), invocation, cwd, firstArg(args), cmd.OutOrStdout())
		},
	})
}

func newRemoveCommand(invocation commands.Invocation) *cobra.Command {
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
			return runDomainRemove(cmd.Context(), invocation, cwd, firstArg(args), opts, cmd.OutOrStdout(), cmd.InOrStdin())
		},
	}
	commands.AddYesFlag(cmd, &opts.yes)
	return commands.DeclareMutating(cmd)
}
