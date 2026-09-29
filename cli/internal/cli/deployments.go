package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
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
		return runPromotionsLs(cmd.Context(), newDeps(), cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

const defaultPruneKeepN = 10

type pruneOptions struct {
	keep int
	yes  bool
}

var pruneOpts pruneOptions

var deploymentsPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Reclaim old production deployments",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		return runPromotionsPrune(cmd.Context(), newDeps(), cwd, pruneOpts, cmd.OutOrStdout(), cmd.InOrStdin())
	},
}

func init() {
	deploymentsCmd.AddCommand(cmddeps.ReserveStdout(deploymentsLsCmd))
	deploymentsPruneCmd.Flags().IntVar(&pruneOpts.keep, "keep", defaultPruneKeepN, "Number of most recent promotions to keep, always additionally pinning the active one")
	cmddeps.Yes(deploymentsPruneCmd, &pruneOpts.yes)
	deploymentsCmd.AddCommand(deploymentsPruneCmd)
}

func runPromotionsLs(ctx context.Context, deps cmddeps.Deps, cwd string, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	promotions, err := listPromotions(ctx, deps, cfg)
	if err != nil {
		return err
	}
	renderPromotions(stdout, promotions)
	return nil
}

func listPromotions(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config) (promotions []*contractv1.PromotionHistoryEntry, err error) {
	if _, err := cfg.RequireProvider(); err != nil {
		return nil, err
	}

	ctx, run, err := deps.Events.Begin(ctx, "ocel deployments ls", cfg.Dir)
	if err != nil {
		return nil, err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := providerclient.Start(ctx, cfg, check, deps.HostTrust, providerclient.PinToLock)
	if err != nil {
		return nil, err
	}
	defer prov.Close()

	err = bootstrap.Ready(ctx, check, prov, cfg, environmentv1.Tier_TIER_PRODUCTION, "ocel bootstrap production")
	check.End(err)
	if err != nil {
		return nil, err
	}

	var listed *contractv1.ListPromotionsResponse
	err = prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		listed, err = client.ListPromotions(ctx, &contractv1.ListPromotionsRequest{
			Slug: cfg.Slug,
			Edge: cfg.EdgeSelection(),
		})
		return err
	})
	return listed.GetPromotions(), err
}

func runPromotionsPrune(ctx context.Context, deps cmddeps.Deps, cwd string, opts pruneOptions, stdout io.Writer, stdin io.Reader) (err error) {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}
	gate := deps.Gate(consent.PlanFirst, "ocel deployments prune", opts.yes, stdout, stdin)
	if err := gate.Refuse(); err != nil {
		return err
	}

	ctx, run, err := deps.Events.Begin(ctx, "ocel deployments prune", cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := providerclient.Start(ctx, cfg, check, deps.HostTrust, providerclient.PinToLock)
	if err != nil {
		return err
	}
	defer prov.Close()

	err = bootstrap.Ready(ctx, check, prov, cfg, environmentv1.Tier_TIER_PRODUCTION, "ocel bootstrap production")
	check.End(err)
	if err != nil {
		return err
	}

	plan := run.Phase(progressv1.Phase_PHASE_PLAN)
	plan.Say(fmt.Sprintf("This will reclaim every production promotion of project %q but the newest %d and the live one; none of them can be rolled back to afterwards", cfg.Slug, opts.keep))
	granted, err := gate.Consent(ctx, plan, nil, fmt.Sprintf("Reclaim the older production promotions of %q?", cfg.Slug))
	plan.End(err)
	if err != nil {
		return err
	}
	if !granted {
		run.Succeed(fmt.Sprintf("Nothing reclaimed: production of %s keeps every promotion", cfg.Slug))
		return nil
	}

	req := &contractv1.RemoveStalePromotionsRequest{
		Slug:  cfg.Slug,
		KeepN: int32(opts.keep),
		Edge:  cfg.EdgeSelection(),
	}
	if _, err := providerclient.Stream(ctx, prov, "RemoveStalePromotions", req, contractv1connect.ProviderServiceClient.RemoveStalePromotions); err != nil {
		return err
	}
	run.Succeed(fmt.Sprintf("Pruned the production promotions of %s down to the newest %d", cfg.Slug, opts.keep))
	return nil
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
