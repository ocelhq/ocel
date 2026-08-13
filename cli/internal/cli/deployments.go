package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/node"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
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
		return runDeploymentsLs(ctx, defaultDeps(), cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
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
		return runDeploymentsPrune(ctx, defaultDeps(), cwd, pruneKeepN, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	deploymentsCmd.AddCommand(deploymentsLsCmd)
	deploymentsPruneCmd.Flags().IntVar(&pruneKeepN, "keep", defaultPruneKeepN, "Number of most recent promotions to keep, always additionally pinning the active one")
	deploymentsCmd.AddCommand(deploymentsPruneCmd)
}

func runDeploymentsLs(ctx context.Context, d deps, cwd string, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd)
	if err != nil {
		return err
	}

	if err := node.Ensure(cfg.Dir); err != nil {
		return err
	}
	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	return runProviderSession(ctx, d, cfg, provider, stdout, stderr, func(runner *providerrunner.Runner) error {
		if err := preflightClass(ctx, d, runner, provider, deploymentsv1.Environment_CLASS_PRODUCTION, "ocel bootstrap", stdout); err != nil {
			return err
		}

		client, err := runner.Deployments()
		if err != nil {
			return err
		}
		resp, err := client.ListPromotions(ctx, &deploymentsv1.ListPromotionsRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Slug:            cfg.Slug,
		})
		if err != nil {
			return err
		}
		renderPromotions(stdout, resp.GetPromotions())
		return nil
	})
}

func runDeploymentsPrune(ctx context.Context, d deps, cwd string, keepN int, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd)
	if err != nil {
		return err
	}

	if err := node.Ensure(cfg.Dir); err != nil {
		return err
	}
	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	ctx, run, err := startRun(ctx, cfg, "ocel deployments prune")
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sessionFormat(), verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, d, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		if err := preflightClass(ctx, d, runner, provider, deploymentsv1.Environment_CLASS_PRODUCTION, "ocel bootstrap", stdout); err != nil {
			return err
		}

		if err := runner.Prune(ctx, &deploymentsv1.PruneRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Slug:            cfg.Slug,
			KeepN:           int32(keepN),
		}, ui.Event); err != nil {
			return err
		}
		ui.Finish("Pruned")
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

func renderPromotions(stdout io.Writer, promotions []*deploymentsv1.PromotionHistoryEntry) {
	if len(promotions) == 0 {
		fmt.Fprintln(stdout, "No promotions yet. Run `ocel deploy` first.")
		return
	}

	activeStatus := "active"
	if deployui.IsTerminal(stdout) {
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
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.GetPromotionId(), tag, epochTimestamp(p.GetTs()), deployedIdentities(p.GetBuilds()), status)
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

func epochTimestamp(sec int64) string {
	if sec == 0 {
		return "—"
	}
	return time.Unix(sec, 0).UTC().Format("2006-01-02 15:04:05 UTC")
}
