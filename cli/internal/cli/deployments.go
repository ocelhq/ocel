package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/preflight"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

var deploymentsCmd = &cobra.Command{
	Use:   "deployments",
	Short: "Manage production deployments",
}

var deploymentsLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List production promotions",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()
		return runPromotionsLs(ctx, newDeps(), cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

const defaultPruneKeepN = 10

var pruneKeepN int

var deploymentsPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Reclaim old production deployments",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()
		return runPromotionsPrune(ctx, newDeps(), cwd, pruneKeepN, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	deploymentsCmd.AddCommand(deploymentsLsCmd)
	deploymentsPruneCmd.Flags().IntVar(&pruneKeepN, "keep", defaultPruneKeepN, "Number of most recent promotions to keep, always additionally pinning the active one")
	deploymentsCmd.AddCommand(deploymentsPruneCmd)
}

func runPromotionsLs(ctx context.Context, deps cmddeps.Deps, cwd string, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	return provider.Drive(ctx, cfg, stdout, stderr, deps.HostTrust, func(runner *provider.Runner) error {
		if err := preflight.Tier(ctx, deps.Presentation(stdout), runner, cfg, environmentv1.Tier_TIER_PRODUCTION, "ocel bootstrap production", stdout); err != nil {
			return err
		}

		client, err := runner.Client()
		if err != nil {
			return err
		}
		resp, err := client.ListPromotions(ctx, &contractv1.ListPromotionsRequest{
			Slug: cfg.Slug,
			Edge: edgewire.Selection(cfg),
		})
		if err != nil {
			return err
		}
		renderPromotions(stdout, resp.GetPromotions())
		return nil
	})
}

func runPromotionsPrune(ctx context.Context, deps cmddeps.Deps, cwd string, keepN int, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	return runui.Run(ctx, deps.Spec(runui.Convergent, "ocel deployments prune", cfg, stdout), func(ctx context.Context, runner *provider.Runner, ui *runui.Session) error {
		if err := preflight.Tier(ctx, ui.Presentation(), runner, cfg, environmentv1.Tier_TIER_PRODUCTION, "ocel bootstrap production", stdout); err != nil {
			return err
		}

		req := &contractv1.RemoveStalePromotionsRequest{
			Slug:  cfg.Slug,
			KeepN: int32(keepN),
			Edge:  edgewire.Selection(cfg),
		}
		if err := provider.Stream(ctx, runner, "RemoveStalePromotions", req, contractv1connect.ProviderServiceClient.RemoveStalePromotions, ui.Event); err != nil {
			return err
		}
		ui.Finish("Pruned")
		return nil
	})
}

func renderPromotions(stdout io.Writer, promotions []*contractv1.PromotionHistoryEntry) {
	if len(promotions) == 0 {
		fmt.Fprintln(stdout, "No promotions yet. Run `ocel deploy` first.")
		return
	}

	activeStatus := "active"
	if runui.IsTerminal(stdout) {
		activeStatus = color.New(color.FgGreen).Sprint("active")
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTAG\tCREATED\tDEPLOYED\tSTATUS")
	for _, entry := range promotions {
		p := entry.GetPromotion()
		tag := p.GetTag()
		if tag == "" {
			tag = "—"
		}
		status := ""
		if entry.GetActive() {
			status = activeStatus
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.GetPromotionId(), tag, runui.EpochDateTime(p.GetTs()), deployedIdentities(p.GetBuilds()), status)
	}
	_ = tw.Flush()
}

func deployedIdentities(identityByApp map[string]string) string {
	if len(identityByApp) == 0 {
		return "—"
	}
	apps := make([]string, 0, len(identityByApp))
	for app := range identityByApp {
		apps = append(apps, app)
	}
	slices.Sort(apps)

	pairs := make([]string, 0, len(apps))
	for _, app := range apps {
		pairs = append(pairs, app+"="+identityByApp[app])
	}
	return strings.Join(pairs, " ")
}
