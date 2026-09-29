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
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
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
		return runRollback(cmd.Context(), newDeps(), cwd, rollbackOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func init() {
	rollbackCmd.Flags().StringVar(&rollbackOpts.to, "to", "", "Roll back to a specific promotion id instead of the immediately previous one")
	rollbackCmd.Flags().StringVar(&rollbackOpts.tag, "tag", "", "Roll back to the promotion with this tag (mutually exclusive with --to)")
	rollbackCmd.Flags().BoolVar(&rollbackOpts.dry, "dry", false, "Print what would be rolled back and stop, rolling back nothing")
	cmddeps.Yes(rollbackCmd, &rollbackOpts.yes)
}

func runRollback(ctx context.Context, deps cmddeps.Deps, cwd string, opts rollbackOptions, stdout, stderr io.Writer, stdin io.Reader) (err error) {
	if opts.to != "" && opts.tag != "" {
		return fmt.Errorf("--to and --tag are mutually exclusive; pass just one")
	}
	cfg, err := deps.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}
	policy := deps.ConsentPolicy("ocel rollback", opts.yes, stdout, stdin)
	policy.ConfirmsPlan = true
	policy.DryRun = opts.dry
	if err := policy.Refuse(); err != nil {
		return err
	}

	ctx, run, err := deps.Events.Begin(ctx, "ocel rollback", cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := providerclient.Start(ctx, cfg, check, deps.Questions, providerclient.ChoosePinning(opts.dry))
	if err != nil {
		return err
	}
	defer prov.Close()

	history, err := promotionHistory(ctx, check, prov, cfg)
	check.End(err)
	if err != nil {
		return err
	}
	live := activePromotion(history)
	target, err := rollbackTarget(history, opts.to, opts.tag)
	if err != nil {
		return err
	}

	plan := run.Phase(progressv1.Phase_PHASE_PLAN)
	showRollbackPlan(plan, cfg.Slug, live, target)
	if opts.dry {
		plan.Say("Run without --dry to roll back.")
		plan.End(nil)
		return nil
	}

	granted, err := policy.ConfirmPlan(ctx, plan, nil, fmt.Sprintf("Roll production of %q back to promotion %s?", cfg.Slug, target.GetPromotionId()))
	plan.End(err)
	if err != nil {
		return err
	}
	if !granted {
		run.Succeed(fmt.Sprintf("Nothing rolled back: production of %s stays on its live promotion", cfg.Slug))
		return nil
	}

	promoting := run.Phase(progressv1.Phase_PHASE_PROMOTE)
	rolled, err := promote(ctx, promoting, prov, cfg, target)
	for _, warning := range rolled.GetWarnings() {
		promoting.Warn(warning)
	}
	promoting.End(err)
	if err != nil {
		return err
	}
	promoted := rolled.GetPromoted()
	tagSuffix := ""
	if target.GetTag() != "" {
		tagSuffix = fmt.Sprintf(", tag %s", target.GetTag())
	}
	flipSuffix := ""
	if note := terminal.PropagationNote(promoted.GetPropagation()); note != "" {
		flipSuffix = "; " + note
	}
	run.Succeed(fmt.Sprintf("Rolled back to promotion %s (created %s%s) as promotion %s%s",
		target.GetPromotionId(), terminal.EpochDate(target.GetTs()), tagSuffix, promoted.GetPromotionId(), flipSuffix))
	return nil
}

func promotionHistory(ctx context.Context, check *run.Span, prov *providerclient.Provider, cfg *project.Project) ([]*contractv1.PromotionHistoryEntry, error) {
	if err := bootstrap.Ready(ctx, check, prov, cfg, environmentv1.Tier_TIER_PRODUCTION, "ocel bootstrap production"); err != nil {
		return nil, err
	}
	unit := check.Unit(cfg.Slug, progress.Reading.Title("the promotion history of production"))
	var listed *contractv1.ListPromotionsResponse
	err := prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		listed, err = client.ListPromotions(ctx, &contractv1.ListPromotionsRequest{
			Slug: cfg.Slug,
			Edge: cfg.EdgeSelection(),
		})
		return err
	})
	unit.End(err)
	return listed.GetPromotions(), err
}

func promote(ctx context.Context, phase *run.Span, prov *providerclient.Provider, cfg *project.Project, target *contractv1.Promotion) (*contractv1.RollbackResponse, error) {
	unit := phase.Unit(cfg.Slug, progress.Switching.Title("production traffic back to promotion "+target.GetPromotionId()))
	var resp *contractv1.RollbackResponse
	err := prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		resp, err = client.Rollback(ctx, &contractv1.RollbackRequest{
			Slug: cfg.Slug,
			To:   target.GetPromotionId(),
			Edge: cfg.EdgeSelection(),
		})
		return err
	})
	unit.End(err)
	return resp, err
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
			if entry.GetPromotion().GetPromotionId() != to {
				continue
			}
			if entry.GetUnpromoted() {
				return nil, fmt.Errorf("promotion %s was taken back when a router could not serve it, so it never served and there is nothing of it to roll back to: `ocel deployments ls` marks it unpromoted", to)
			}
			return entry.GetPromotion(), nil
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
		for _, earlier := range history[i+1:] {
			if !earlier.GetUnpromoted() {
				return earlier.GetPromotion(), nil
			}
		}
		return nil, fmt.Errorf("promotion %s is live and no earlier promotion in this project's production history served, so there is nothing earlier to roll back to", entry.GetPromotion().GetPromotionId())
	}
	return nil, fmt.Errorf("no promotion in this project's production history is live, so there is nothing to roll back from: pass --to with the promotion id to serve")
}

func showRollbackPlan(plan *run.Span, slug string, live, target *contractv1.Promotion) {
	lines := []string{fmt.Sprintf("This will roll production of project %q back to an earlier deployment", slug)}
	if live != nil {
		lines = append(lines, "– live    "+promotionLine(live))
	}
	lines = append(lines, "– target  "+promotionLine(target))
	if note := terminal.PropagationNote(target.GetPropagation()); note != "" {
		lines = append(lines, note)
	}
	lines = append(lines, "`ocel deploy` puts the current build back.")
	plan.Say(strings.Join(lines, "\n"))
}

func promotionLine(p *contractv1.Promotion) string {
	tag := "untagged"
	if p.GetTag() != "" {
		tag = "tag " + p.GetTag()
	}
	return fmt.Sprintf("%s  created %s  %s  %s", p.GetPromotionId(), terminal.EpochDateTime(p.GetTs()), tag, deployedIdentities(p.GetBuilds()))
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
