package promotions

import (
	"context"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func NewDeploymentsCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deployments",
		Short: "Manage production deployments",
	}
	cmd.AddCommand(commands.ReserveStdout(newListCommand(invocation)), newPruneCommand(invocation))
	return commands.DeclareReadOnly(cmd)
}

func newListCommand(invocation commands.Invocation) *cobra.Command {
	return commands.DeclareReadOnly(&cobra.Command{
		Use:   "ls",
		Short: "List production promotions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return runPromotionsList(cmd.Context(), invocation, cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	})
}

const defaultPruneKeepN = 10

type pruneOptions struct {
	keep int
	yes  bool
}

func newPruneCommand(invocation commands.Invocation) *cobra.Command {
	var opts pruneOptions
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Reclaim old production deployments",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return runPromotionsPrune(cmd.Context(), invocation, cwd, opts, cmd.OutOrStdout(), cmd.InOrStdin())
		},
	}
	cmd.Flags().IntVar(&opts.keep, "keep", defaultPruneKeepN, "Number of most recent promotions to keep, always additionally pinning the active one")
	commands.AddYesFlag(cmd, &opts.yes)
	return commands.DeclareMutating(cmd)
}

func runPromotionsList(ctx context.Context, invocation commands.Invocation, cwd string, stdout, stderr io.Writer) error {
	cfg, err := invocation.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	promotions, err := listPromotions(ctx, invocation, cfg)
	if err != nil {
		return err
	}
	if invocation.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, deploymentListResult(promotions))
	}
	renderPromotions(stdout, promotions)
	return nil
}

func deploymentListResult(promotions []*contractv1.PromotionHistoryEntry) *resultv1.DeploymentListResult {
	result := &resultv1.DeploymentListResult{Deployments: make([]*resultv1.DeploymentSummary, 0, len(promotions))}
	for _, entry := range promotions {
		p := entry.GetPromotion()
		result.Deployments = append(result.Deployments, &resultv1.DeploymentSummary{
			PromotionId: p.GetPromotionId(),
			Tag:         p.GetTag(),
			CreatedAt:   terminal.EpochRFC3339(p.GetTs()),
			Builds:      p.GetBuilds(),
			State:       deploymentState(entry),
		})
	}
	return result
}

func deploymentState(entry *contractv1.PromotionHistoryEntry) resultv1.DeploymentState {
	switch {
	case entry.GetActive():
		return resultv1.DeploymentState_DEPLOYMENT_STATE_ACTIVE
	case entry.GetUnpromoted():
		return resultv1.DeploymentState_DEPLOYMENT_STATE_UNPROMOTED
	default:
		return resultv1.DeploymentState_DEPLOYMENT_STATE_SUPERSEDED
	}
}

func listPromotions(ctx context.Context, invocation commands.Invocation, cfg *project.Project) (promotions []*contractv1.PromotionHistoryEntry, err error) {
	err = invocation.WithProvider(ctx, cfg, "ocel deployments ls", commands.OpenOptions{Tier: environmentv1.Tier_TIER_PRODUCTION, Require: readiness.Features}, func(ctx context.Context, p commands.ProviderRun) error {
		p.Check.End(nil)
		return p.Provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
			listed, err := client.ListPromotions(ctx, &contractv1.ListPromotionsRequest{
				Slug: cfg.Slug,
				Edge: cfg.EdgeSelection(),
			})
			promotions = listed.GetPromotions()
			return err
		})
	})
	return promotions, err
}

func runPromotionsPrune(ctx context.Context, invocation commands.Invocation, cwd string, opts pruneOptions, stdout io.Writer, stdin io.Reader) (err error) {
	cfg, err := invocation.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	policy := consent.NewPlanPolicy("ocel deployments prune", opts.yes, invocation.CanAsk(stdin), stdout, stdin)
	if err := policy.Refuse(); err != nil {
		return err
	}

	return invocation.WithProvider(ctx, cfg, policy.Command, commands.OpenOptions{Tier: environmentv1.Tier_TIER_PRODUCTION, Require: readiness.Features}, func(ctx context.Context, p commands.ProviderRun) error {
		p.Check.End(nil)
		plan := p.Run.Phase(progressv1.Phase_PHASE_PLAN)
		plan.Say(fmt.Sprintf("This will reclaim every production promotion of project %q but the newest %d and the live one; none of them can be rolled back to afterwards", cfg.Slug, opts.keep))
		granted, err := policy.ConfirmPlan(ctx, plan, nil, fmt.Sprintf("Reclaim the older production promotions of %q?", cfg.Slug))
		plan.End(err)
		if err != nil {
			return err
		}
		if !granted {
			p.Run.Succeed(fmt.Sprintf("Nothing reclaimed: production of %s keeps every promotion", cfg.Slug))
			return nil
		}

		req := &contractv1.RemoveStalePromotionsRequest{
			Slug:  cfg.Slug,
			KeepN: int32(opts.keep),
			Edge:  cfg.EdgeSelection(),
		}
		if _, err := providerprocess.Stream(ctx, p.Provider, "RemoveStalePromotions", req, contractv1connect.ProviderServiceClient.RemoveStalePromotions); err != nil {
			return err
		}
		p.Run.Succeed(fmt.Sprintf("Pruned the production promotions of %s down to the newest %d", cfg.Slug, opts.keep))
		return nil
	})
}

func renderPromotions(stdout io.Writer, promotions []*contractv1.PromotionHistoryEntry) {
	if len(promotions) == 0 {
		fmt.Fprintln(stdout, "No promotions yet. Run `ocel deploy` first.")
		return
	}

	activeStatus := terminal.PaletteFor(stdout).Success("active")

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTAG\tCREATED\tDEPLOYED\tSTATUS")
	for _, entry := range promotions {
		p := entry.GetPromotion()
		tag := p.GetTag()
		if tag == "" {
			tag = "—"
		}
		status := ""
		switch {
		case entry.GetActive():
			status = activeStatus
		case entry.GetUnpromoted():
			status = "unpromoted"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.GetPromotionId(), tag, terminal.EpochDateTime(p.GetTs()), deployedIdentities(p.GetBuilds()), status)
	}
	_ = tw.Flush()
}
