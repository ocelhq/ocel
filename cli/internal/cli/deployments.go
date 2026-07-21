package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/fatih/color"
	"github.com/mattn/go-isatty"
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

// renderPromotions prints the promotion history as an aligned ID/TAG/CREATED/
// STATUS table, newest-first (the order the store returns), with the active
// promotion's STATUS shown as a green "active". The colored cell is last so its
// ANSI escapes — which tabwriter counts as width — never misalign a later
// column, and color is emitted only to a terminal.
func renderPromotions(stdout io.Writer, promotions []*deploymentsv1.PromotionHistoryEntry) {
	if len(promotions) == 0 {
		fmt.Fprintln(stdout, "No promotions yet. Run `ocel deploy` first.")
		return
	}

	activeStatus := "active"
	if isWriterTTY(stdout) {
		activeStatus = color.New(color.FgGreen).Sprint("active")
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTAG\tCREATED\tSTATUS")
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
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.GetPromotionId(), tag, epochTimestamp(p.GetTs()), status)
	}
	_ = tw.Flush()
}

// isWriterTTY reports whether w is an interactive terminal, so table output
// only emits ANSI color when a human will see it (never into a pipe or file).
func isWriterTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && isatty.IsTerminal(f.Fd())
}

// epochTimestamp renders an epoch-seconds time as a full UTC timestamp, or "—"
// when the provider reported 0 (unknown). Distinct from epochOrDash (date
// only), which the preview commands still use.
func epochTimestamp(sec int64) string {
	if sec == 0 {
		return "—"
	}
	return time.Unix(sec, 0).UTC().Format("2006-01-02 15:04:05 UTC")
}
