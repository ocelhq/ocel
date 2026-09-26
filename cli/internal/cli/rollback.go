package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/runui"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type rollbackOptions struct {
	to  string
	tag string
	yes bool
	dry bool
}

var rollbackOpts rollbackOptions

var rollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "Roll production back to a previous deployment",
	Long: "Roll production back to a previous deployment.\n\n" +
		"Every run prints the promotion that is live and the promotion it would put in its place " +
		"before asking to proceed; --dry prints it and stops.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()
		return runRollback(ctx, newDeps(), cwd, rollbackOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func init() {
	rollbackCmd.Flags().StringVar(&rollbackOpts.to, "to", "", "Roll back to a specific promotion id instead of the immediately previous one")
	rollbackCmd.Flags().StringVar(&rollbackOpts.tag, "tag", "", "Roll back to the promotion with this tag (mutually exclusive with --to)")
	rollbackCmd.Flags().BoolVar(&rollbackOpts.dry, "dry", false, "Print what would be rolled back and stop, rolling back nothing")
	cmddeps.Yes(rollbackCmd, &rollbackOpts.yes)
}

func runRollback(ctx context.Context, deps cmddeps.Deps, cwd string, opts rollbackOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	if opts.to != "" && opts.tag != "" {
		return fmt.Errorf("--to and --tag are mutually exclusive; pass just one")
	}
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	spec := deps.Spec(runui.PlanFirst, "ocel rollback", cfg, opts.yes, stdout, stdin)
	spec.Dry = opts.dry
	spec.Unattended = "pass --yes"

	return runui.Run(ctx, spec, func(ctx context.Context, runner *providerclient.Runner, ui *runui.Session) error {
		if err := bootstrap.Ready(ctx, ui, runner, cfg, environmentv1.Tier_TIER_PRODUCTION, "ocel bootstrap production"); err != nil {
			return err
		}

		client, err := runner.Client()
		if err != nil {
			return err
		}

		spinner := ui.Spin("Reading this project's promotion history")
		listed, err := client.ListPromotions(ctx, &contractv1.ListPromotionsRequest{
			Slug: cfg.Slug,
			Edge: edgewire.Selection(cfg),
		})
		spinner.Stop()
		if err != nil {
			return err
		}

		history := listed.GetPromotions()
		live := activePromotion(history)
		target, err := rollbackTarget(history, opts.to, opts.tag)
		if err != nil {
			return err
		}

		showRollbackPlan(ui, cfg.Slug, live, target)
		if opts.dry {
			ui.Diagnostic("Run without --dry to roll back.")
			return nil
		}

		granted, err := ui.Consent(ctx, fmt.Sprintf("Roll production of %q back to promotion %s?", cfg.Slug, target.GetPromotionId()))
		if err != nil || !granted {
			return err
		}

		resp, err := client.Rollback(ctx, &contractv1.RollbackRequest{
			Slug: cfg.Slug,
			To:   target.GetPromotionId(),
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
		if note := runui.FlipNote(promoted.GetFlipBound()); note != "" {
			flipSuffix = "; " + note
		}
		ui.Finish(fmt.Sprintf("Rolled back to promotion %s (created %s%s)%s", promoted.GetPromotionId(), runui.EpochDate(promoted.GetTs()), tagSuffix, flipSuffix))
		return nil
	})
}

func activePromotion(history []*contractv1.PromotionHistoryEntry) *contractv1.Promotion {
	for _, entry := range history {
		if entry.GetActive() {
			return entry.GetPromotion()
		}
	}
	return nil
}

func rollbackTarget(history []*contractv1.PromotionHistoryEntry, to, tag string) (*contractv1.Promotion, error) {
	if len(history) == 0 {
		return nil, fmt.Errorf("this project has no promotions in its production history, so there is nothing to roll back to: run `ocel deploy` first")
	}
	switch {
	case to != "":
		for _, entry := range history {
			if entry.GetPromotion().GetPromotionId() == to {
				return entry.GetPromotion(), nil
			}
		}
		return nil, fmt.Errorf("no promotion %q in this project's production history, which contains %s: `ocel deployments ls` lists them all", to, promotionIDs(history))
	case tag != "":
		var matched []*contractv1.Promotion
		for _, entry := range history {
			if entry.GetPromotion().GetTag() == tag {
				matched = append(matched, entry.GetPromotion())
			}
		}
		switch len(matched) {
		case 1:
			return matched[0], nil
		case 0:
			return nil, fmt.Errorf("no promotion in this project's production history has tag %q; the tags it has are %s", tag, promotionTags(history))
		default:
			return nil, fmt.Errorf("tag %q is on %d promotions (%s), so it does not name one to roll back to: pass --to with the promotion id", tag, len(matched), strings.Join(idsOf(matched), ", "))
		}
	}
	for i, entry := range history {
		if !entry.GetActive() {
			continue
		}
		if i+1 >= len(history) {
			return nil, fmt.Errorf("promotion %s is live and is the earliest in this project's production history, so there is nothing earlier to roll back to", entry.GetPromotion().GetPromotionId())
		}
		return history[i+1].GetPromotion(), nil
	}
	return nil, fmt.Errorf("no promotion in this project's production history is live, so there is nothing to roll back from: pass --to with the promotion id to serve")
}

func showRollbackPlan(ui *runui.Session, slug string, live, target *contractv1.Promotion) {
	lines := []string{fmt.Sprintf("This will roll production of project %q back to an earlier deployment", slug)}
	if live != nil {
		lines = append(lines, "– live    "+promotionLine(live))
	}
	lines = append(lines, "– target  "+promotionLine(target))
	if note := runui.FlipNote(target.GetFlipBound()); note != "" {
		lines = append(lines, note)
	}
	lines = append(lines, "`ocel deploy` puts the current build back.")
	ui.Diagnostic(strings.Join(lines, "\n"))
}

func promotionLine(p *contractv1.Promotion) string {
	tag := "untagged"
	if p.GetTag() != "" {
		tag = "tag " + p.GetTag()
	}
	return fmt.Sprintf("%s  created %s  %s  %s", p.GetPromotionId(), runui.EpochDateTime(p.GetTs()), tag, deployedIdentities(p.GetBuilds()))
}

func promotionIDs(history []*contractv1.PromotionHistoryEntry) string {
	ids := make([]string, 0, len(history))
	for _, entry := range history {
		ids = append(ids, entry.GetPromotion().GetPromotionId())
	}
	return elided(ids)
}

func promotionTags(history []*contractv1.PromotionHistoryEntry) string {
	var tags []string
	for _, entry := range history {
		if tag := entry.GetPromotion().GetTag(); tag != "" {
			tags = append(tags, tag)
		}
	}
	if len(tags) == 0 {
		return "none"
	}
	return elided(tags)
}

func idsOf(promotions []*contractv1.Promotion) []string {
	ids := make([]string, 0, len(promotions))
	for _, p := range promotions {
		ids = append(ids, p.GetPromotionId())
	}
	return ids
}

const rollbackListCap = 10

func elided(values []string) string {
	if len(values) <= rollbackListCap {
		return strings.Join(values, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(values[:rollbackListCap], ", "), len(values)-rollbackListCap)
}
