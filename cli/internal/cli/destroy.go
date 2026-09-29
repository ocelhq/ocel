package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

var destroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "Permanently destroy this project's deployment of one tier",
	Long: "Permanently destroy what this project has deployed into one tier: `production` takes " +
		"what the edge serves it with and the hostnames bound to it, its resources (databases and " +
		"buckets, including all their data), and every app; `preview` " +
		"takes the whole preview footprint and leaves the account-level preview bootstrap intact.\n\n" +
		"Either is irreversible and requires typing the project name to confirm; --dry prints " +
		"what would go and stops.\n\n" +
		"An automated caller that must tear its own project down unattended passes --yes, or sets " +
		consent.BypassEnv + " to the project name — and only that name. " +
		"Any other value is not a bypass.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("the tier to destroy is production or preview, not %q", args[0])
		}
		_ = cmd.Help()
		return errors.New("destroy acts on one tier at a time: production or preview")
	},
}

var destroyProductionCmd = &cobra.Command{
	Use:     "production",
	Aliases: []string{"prod"},
	Short:   "Permanently destroy this project's entire production deployment",
	Long: "Permanently destroy this project's entire production deployment.\n\n" +
		"Every run prints what it would destroy before asking for the project name; --dry prints " +
		"it and stops.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		return runDestroyProduction(cmd.Context(), newDeps(), cwd, destroyProductionYes, destroyProductionDry, cmd.OutOrStdout(), cmd.InOrStdin())
	},
}

var (
	destroyPreviewYes    bool
	destroyProductionYes bool
	destroyProductionDry bool
	destroyPreviewDry    bool
)

func init() {
	previewCmd := &cobra.Command{
		Use:   "preview",
		Short: "Permanently destroy this project's entire preview footprint",
		Long: "Permanently destroy this project's entire preview footprint: every preview, all its " +
			"data, assets and variables. The account-level preview bootstrap is left intact.\n\n" +
			"Every run prints what it would destroy before asking for the project name; --dry prints " +
			"it and stops.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			return runDestroyPreviewProject(cmd.Context(), newDeps(), cwd, destroyPreviewYes, destroyPreviewDry, cmd.OutOrStdout(), cmd.InOrStdin())
		},
	}
	cmddeps.Yes(previewCmd, &destroyPreviewYes)
	previewCmd.Flags().BoolVar(&destroyPreviewDry, "dry", false, "Print what would be destroyed and stop, destroying nothing")
	cmddeps.Yes(destroyProductionCmd, &destroyProductionYes)
	destroyProductionCmd.Flags().BoolVar(&destroyProductionDry, "dry", false, "Print what would be destroyed and stop, destroying nothing")

	destroyCmd.AddCommand(destroyProductionCmd, previewCmd)
	rootCmd.AddCommand(destroyCmd)
}

func runDestroyProduction(ctx context.Context, deps cmddeps.Deps, cwd string, yes, dry bool, stdout io.Writer, stdin io.Reader) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	bypass, notice, err := consent.Bypass{
		Noun:    "project",
		Subject: cfg.Slug,
		Action:  "destroying production",
		Verb:    "destroyed",
		Yes:     yes,
		Dry:     dry,
		TTY:     deps.StdinIsTerminal(stdin),
	}.Granted()
	if err != nil {
		return err
	}

	gate := deps.Gate(consent.PlanFirst, "ocel destroy production", yes || bypass, stdout, stdin)
	gate.Dry = dry
	gate.Unattended = fmt.Sprintf("pass --yes, or set %s to the project name", consent.BypassEnv)
	return destroyProject(ctx, deps, cfg, gate, environmentv1.Tier_TIER_PRODUCTION, notice)
}

func runDestroyPreviewProject(ctx context.Context, deps cmddeps.Deps, cwd string, yes, dry bool, stdout io.Writer, stdin io.Reader) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	gate := deps.Gate(consent.PlanFirst, "ocel destroy preview", yes, stdout, stdin)
	gate.Dry = dry
	gate.Unattended = "pass --yes"
	return destroyProject(ctx, deps, cfg, gate, environmentv1.Tier_TIER_PREVIEW, "")
}

func destroyProject(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, gate consent.Gate, tier environmentv1.Tier, bypassNotice string) (err error) {
	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}
	if err := gate.Refuse(); err != nil {
		return err
	}

	ctx, run, err := deps.Events.Begin(ctx, gate.Command, cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	if bypassNotice != "" {
		check.Warn(bypassNotice)
	}
	prov, err := providerclient.Start(ctx, cfg, check, deps.HostTrust, providerclient.ChoosePinning(gate.Dry))
	if err != nil {
		return err
	}
	defer prov.Close()

	err = bootstrap.Ready(ctx, check, prov, cfg, tier, "ocel bootstrap "+bootstrap.Name(tier))
	check.End(err)
	if err != nil {
		return err
	}

	preview := tier == environmentv1.Tier_TIER_PREVIEW
	var env *environmentv1.Environment
	if preview {
		env = &environmentv1.Environment{Tier: tier}
	}

	place := "production"
	if preview {
		place = "any preview"
	}
	planning := run.Phase(progressv1.Phase_PHASE_PLAN)
	unit := planning.Unit(cfg.Slug, fmt.Sprintf("Enumerating what %s has in %s to destroy", cfg.Slug, place))
	var plan *planv1.ChangePlan
	err = prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		plan, err = client.PlanRemoveProject(ctx, &contractv1.ProjectRequest{
			Slug:        cfg.Slug,
			Environment: env,
			Edge:        edgewire.Selection(cfg),
		})
		return err
	})
	unit.End(err)
	if err != nil {
		return err
	}
	if len(plan.GetGroups()) == 0 {
		run.Finish(fmt.Sprintf("Nothing to destroy: %s has nothing in %s", cfg.Slug, place))
		return nil
	}

	consented := showDestroyPlan(planning, cfg.Slug, preview, plan)
	if gate.Dry {
		planning.Say("Run without --dry to destroy.")
		run.Finish(fmt.Sprintf("Planned the destroy of what %s has in %s", cfg.Slug, place))
		return nil
	}
	granted, err := gate.ConsentByName(ctx, planning, consented, "project name", plan.GetSubject())
	planning.End(err)
	if err != nil {
		return err
	}
	if !granted {
		run.Finish(fmt.Sprintf("Nothing destroyed: what %s has in %s stays", cfg.Slug, place))
		return nil
	}

	req := &contractv1.ProjectRequest{
		Slug:        cfg.Slug,
		Environment: env,
		Edge:        edgewire.Selection(cfg),
		Consented:   consented,
	}
	if _, err := providerclient.Stream(ctx, prov, "RemoveProject", req, contractv1connect.ProviderServiceClient.RemoveProject); err != nil {
		return err
	}
	if preview {
		run.Finish(fmt.Sprintf("Destroyed preview footprint of project %s", cfg.Slug))
		return nil
	}
	run.Finish(fmt.Sprintf("Destroyed project %s", cfg.Slug))
	return nil
}

func showDestroyPlan(planning *events.Scope, slug string, preview bool, plan *planv1.ChangePlan) *planv1.ChangePlan {
	if preview {
		return planning.Plan(fmt.Sprintf("This will permanently destroy the ENTIRE preview footprint of project %q", slug), plan,
			"– all stored preview assets belonging to this project",
			"– every preview variable value this project has, including each preview's own overrides",
			"The account-level preview bootstrap is left intact. This cannot be undone.")
	}
	return planning.Plan(fmt.Sprintf("This will permanently destroy production project %q", slug), plan,
		"– all stored assets belonging to this project",
		"– every production variable value this project has, and their history",
		"This cannot be undone.")
}
