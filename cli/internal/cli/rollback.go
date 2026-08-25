package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/session"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type rollbackOptions struct {
	to  string
	tag string
}

var rollbackOpts rollbackOptions

var rollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "Roll production back to a previous deployment",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()
		return runRollback(ctx, newSession(), cwd, rollbackOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	rollbackCmd.Flags().StringVar(&rollbackOpts.to, "to", "", "Roll back to a specific promotion id instead of the immediately previous one")
	rollbackCmd.Flags().StringVar(&rollbackOpts.tag, "tag", "", "Roll back to the promotion carrying this tag (mutually exclusive with --to)")
}

func runRollback(ctx context.Context, d session.Session, cwd string, opts rollbackOptions, stdout, stderr io.Writer) error {
	if opts.to != "" && opts.tag != "" {
		return fmt.Errorf("--to and --tag are mutually exclusive; pass just one")
	}
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	ctx, run, err := runtrace.Start(ctx, cfg.Dir, "ocel rollback")
	if err != nil {
		return err
	}
	defer run.Close()

	return provider.Drive(ctx, cfg, stdout, stderr, func(runner *provider.Runner) error {
		if err := preflightTier(ctx, d, runner, cfg, environmentv1.Tier_TIER_PRODUCTION, "ocel bootstrap production", stdout); err != nil {
			return err
		}

		client, err := runner.Deployments()
		if err != nil {
			return err
		}
		resp, err := client.Rollback(ctx, &contractv1.RollbackRequest{
			Slug: cfg.Slug,
			To:   opts.to,
			Tag:  opts.tag,
			Edge: edgewire.Selection(cfg),
		})
		if err != nil {
			return err
		}

		promoted := resp.GetPromoted()
		tagSuffix := ""
		if promoted.GetTag() != "" {
			tagSuffix = fmt.Sprintf(", tag %s", promoted.GetTag())
		}
		flipSuffix := ""
		if note := flipNote(promoted.GetFlipBound()); note != "" {
			flipSuffix = "; " + note
		}
		fmt.Fprintf(stdout, "Rolled back to promotion %s (created %s%s)%s\n", promoted.GetPromotionId(), epochOrDash(promoted.GetTs()), tagSuffix, flipSuffix)
		return nil
	})
}
