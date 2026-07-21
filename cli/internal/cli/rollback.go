package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// rollbackOptions holds the flags accepted by `ocel rollback`.
type rollbackOptions struct {
	to  string
	tag string
}

var rollbackOpts rollbackOptions

// rollbackCmd re-points production at a previous promotion (ADR 0001):
// project-wide, atomic, and production-only — it refuses on preview
// infrastructure the same way `ocel deploy` does.
var rollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "Roll production back to a previous deployment",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runRollback(ctx, cwd, rollbackOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	rollbackCmd.Flags().StringVar(&rollbackOpts.to, "to", "", "Roll back to a specific promotion id instead of the immediately previous one")
	rollbackCmd.Flags().StringVar(&rollbackOpts.tag, "tag", "", "Roll back to the promotion carrying this tag (mutually exclusive with --to)")
}

// runRollback resolves the project, preflights production infrastructure, and
// drives the provider's Rollback RPC: no flag rolls back to the promotion
// immediately before the currently active one; --to <promotionId> targets a
// specific one; --tag <tag> targets the promotion carrying that tag. --to and
// --tag are mutually exclusive. A rolled-back promotion is itself still in
// history, so running rollback again rolls forward.
func runRollback(ctx context.Context, cwd string, opts rollbackOptions, stdout, stderr io.Writer) error {
	if opts.to != "" && opts.tag != "" {
		return fmt.Errorf("--to and --tag are mutually exclusive; pass just one")
	}
	if err := validateTag(opts.tag); err != nil {
		return err
	}

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

		resp, err := runner.Rollback(ctx, &deploymentsv1.RollbackRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			ProjectId:       cfg.ProjectID,
			To:              opts.to,
			Tag:             opts.tag,
		})
		if err != nil {
			return err
		}

		promoted := resp.GetPromoted()
		tagSuffix := ""
		if promoted.GetTag() != "" {
			tagSuffix = fmt.Sprintf(", tag %s", promoted.GetTag())
		}
		fmt.Fprintf(stdout, "Rolled back to promotion %s (created %s%s)\n", promoted.GetPromotionId(), epochOrDash(promoted.GetTs()), tagSuffix)
		return nil
	})
}
