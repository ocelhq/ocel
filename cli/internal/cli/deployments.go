package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// deploymentsCmd groups production-deployment (promotion) management
// subcommands.
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
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runDeploymentsLs(ctx, cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

// defaultPruneKeepN is `ocel deployments prune`'s default retention window
// (in Promotions) when --keep is not given.
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
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runDeploymentsPrune(ctx, cwd, pruneKeepN, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	deploymentsCmd.AddCommand(deploymentsLsCmd)
	deploymentsPruneCmd.Flags().IntVar(&pruneKeepN, "keep", defaultPruneKeepN, "Number of most recent promotions to keep, always additionally pinning the active one")
	deploymentsCmd.AddCommand(deploymentsPruneCmd)
}

// runDeploymentsLs resolves the project, preflights production infrastructure,
// and drives the provider's ListPromotions RPC, rendering each promotion
// newest-first with its active marker.
func runDeploymentsLs(ctx context.Context, cwd string, stdout, stderr io.Writer) error {
	if _, err := loadCredentials(); err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &ExitError{Code: 1}
	}

	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}
	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	return runProviderSession(ctx, cfg, provider, stdout, stderr, func(runner *providerrunner.Runner) error {
		if err := preflightClass(ctx, runner, provider, deploymentsv1.Environment_CLASS_PRODUCTION, "ocel bootstrap", stdout); err != nil {
			return err
		}

		resp, err := runner.ListPromotions(ctx, &deploymentsv1.ListPromotionsRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			ProjectId:       cfg.ProjectID,
		})
		if err != nil {
			return err
		}
		renderPromotions(stdout, resp.GetPromotions())
		return nil
	})
}

// runDeploymentsPrune resolves the project, preflights production
// infrastructure, and drives the provider's Prune RPC: keepN is how many of
// the most recent promotions to keep, always additionally pinning the
// active one. It never runs as part of `ocel deploy` — it is a standalone
// command the user runs explicitly.
func runDeploymentsPrune(ctx context.Context, cwd string, keepN int, stdout, stderr io.Writer) error {
	if _, err := loadCredentials(); err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &ExitError{Code: 1}
	}

	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}
	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	ui := deployui.New(stdout, cfg.Dir, "ocel deployments prune", verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		if err := preflightClass(ctx, runner, provider, deploymentsv1.Environment_CLASS_PRODUCTION, "ocel bootstrap", stdout); err != nil {
			return err
		}

		if err := runner.Prune(ctx, &deploymentsv1.PruneRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			ProjectId:       cfg.ProjectID,
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

// renderPromotions prints one line per promotion, newest-first, marking the
// active one.
func renderPromotions(stdout io.Writer, promotions []*deploymentsv1.PromotionHistoryEntry) {
	if len(promotions) == 0 {
		fmt.Fprintln(stdout, "No promotions yet. Run `ocel deploy` first.")
		return
	}
	for _, entry := range promotions {
		marker := " "
		if entry.GetActive() {
			marker = "*"
		}
		p := entry.GetPromotion()
		fmt.Fprintf(stdout, "%s %s\tcreated %s\n", marker, p.GetPromotionId(), epochOrDash(p.GetTs()))
	}
}
