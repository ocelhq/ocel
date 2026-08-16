package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/node"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
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
		return runRollback(ctx, defaultDeps(), cwd, rollbackOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	rollbackCmd.Flags().StringVar(&rollbackOpts.to, "to", "", "Roll back to a specific promotion id instead of the immediately previous one")
	rollbackCmd.Flags().StringVar(&rollbackOpts.tag, "tag", "", "Roll back to the promotion carrying this tag (mutually exclusive with --to)")
}

func runRollback(ctx context.Context, d deps, cwd string, opts rollbackOptions, stdout, stderr io.Writer) error {
	if opts.to != "" && opts.tag != "" {
		return fmt.Errorf("--to and --tag are mutually exclusive; pass just one")
	}
	if err := validateTag(opts.tag); err != nil {
		return err
	}

	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
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

	ctx, run, err := startRun(ctx, cfg, "ocel rollback")
	if err != nil {
		return err
	}
	defer run.Close()

	return runProviderSession(ctx, d, cfg, provider, stdout, stderr, func(runner *providerrunner.Runner) error {
		if err := preflightClass(ctx, d, runner, provider, deploymentsv1.Environment_CLASS_PRODUCTION, "ocel bootstrap", stdout); err != nil {
			return err
		}

		client, err := runner.Deployments()
		if err != nil {
			return err
		}
		resp, err := client.Rollback(ctx, &deploymentsv1.RollbackRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Slug:            cfg.Slug,
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
