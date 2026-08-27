package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/preflight"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

var destroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "Permanently destroy this project's deployment of one class",
	Long: "Permanently destroy what this project has deployed into one class: `production` takes " +
		"the edge stack (edge workers, custom-domain binding, deployments store), the infra stack " +
		"(databases and buckets, including all their data), and every app-deploy stack; `preview` " +
		"takes the whole preview footprint and leaves the account-level preview bootstrap intact.\n\n" +
		"Either is irreversible and requires typing the project name to confirm; --dry prints " +
		"what would go and stops.\n\n" +
		"An automated caller that must tear its own project down unattended passes --yes, or sets " +
		runui.BypassEnv + " to the project name — and only that name. " +
		"Any other value is not a bypass.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("the class to destroy is production or preview, not %q", args[0])
		}
		_ = cmd.Help()
		return errors.New("destroy acts on one class at a time: production or preview")
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

		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		return runDestroyProduction(ctx, newDeps(), cwd, destroyProductionYes, destroyProductionDry, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
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

			ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
			defer stop()

			return runDestroyPreviewProject(ctx, newDeps(), cwd, destroyPreviewYes, destroyPreviewDry, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}
	cmddeps.Yes(previewCmd, &destroyPreviewYes)
	previewCmd.Flags().BoolVar(&destroyPreviewDry, "dry", false, "Print what would be destroyed and stop, destroying nothing")
	cmddeps.Yes(destroyProductionCmd, &destroyProductionYes)
	destroyProductionCmd.Flags().BoolVar(&destroyProductionDry, "dry", false, "Print what would be destroyed and stop, destroying nothing")

	destroyCmd.AddCommand(destroyProductionCmd, previewCmd)
	rootCmd.AddCommand(destroyCmd)
}

func runDestroyProduction(ctx context.Context, deps cmddeps.Deps, cwd string, yes, dry bool, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	bypass, err := runui.Bypass{
		Noun:    "project",
		Subject: cfg.Slug,
		Action:  "destroying production",
		Verb:    "destroyed",
		Yes:     yes,
		Dry:     dry,
		TTY:     deps.StdinIsTerminal(stdin),
	}.Granted(stderr)
	if err != nil {
		return err
	}

	spec := deps.Spec(runui.PlanFirst, "ocel destroy production", cfg, yes || bypass, stdout, stdin)
	spec.Dry = dry
	spec.Unattended = fmt.Sprintf("pass --yes, or set %s to the project name", runui.BypassEnv)

	return runui.Run(ctx, spec, func(ctx context.Context, runner *provider.Runner, ui *runui.Session) error {
		if err := preflight.Tier(ctx, ui.Presentation(), runner, cfg, environmentv1.Tier_TIER_PRODUCTION, "ocel bootstrap production", stdout); err != nil {
			return err
		}

		client, err := runner.Client()
		if err != nil {
			return err
		}

		spinner := runui.StartSpinner(ui.Presentation(), stdout, "Enumerating what would be destroyed")
		plan, err := client.PlanRemoveProject(ctx, &contractv1.ProjectRequest{
			Slug: cfg.Slug,
			Edge: edgewire.Selection(cfg),
		})
		spinner.Stop()
		if err != nil {
			return err
		}
		if len(plan.GetGroups()) == 0 {
			ui.Finish("Nothing to destroy")
			return nil
		}

		consented := showDestroyPlan(ui, cfg.Slug, false, plan)
		if dry {
			ui.Diagnostic("Run without --dry to destroy.")
			return nil
		}
		granted, err := ui.ConsentByName(ctx, "project name", plan.GetSubject())
		if err != nil || !granted {
			return err
		}

		req := &contractv1.ProjectRequest{
			Slug:      cfg.Slug,
			Edge:      edgewire.Selection(cfg),
			Consented: consented,
		}
		if err := provider.Stream(ctx, runner, "RemoveProject", req, contractv1connect.ProviderServiceClient.RemoveProject, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Destroyed project %s", cfg.Slug))
		return nil
	})
}

func runDestroyPreviewProject(ctx context.Context, deps cmddeps.Deps, cwd string, yes, dry bool, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	spec := deps.Spec(runui.PlanFirst, "ocel destroy preview", cfg, yes, stdout, stdin)
	spec.Dry = dry
	spec.Unattended = "pass --yes"

	return runui.Run(ctx, spec, func(ctx context.Context, runner *provider.Runner, ui *runui.Session) error {
		if err := preflight.Tier(ctx, ui.Presentation(), runner, cfg, environmentv1.Tier_TIER_PREVIEW, "ocel bootstrap preview", stdout); err != nil {
			return err
		}

		client, err := runner.Client()
		if err != nil {
			return err
		}

		spinner := runui.StartSpinner(ui.Presentation(), stdout, "Enumerating what would be destroyed")
		plan, err := client.PlanRemoveProject(ctx, &contractv1.ProjectRequest{
			Slug:        cfg.Slug,
			Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW},
			Edge:        edgewire.Selection(cfg),
		})
		spinner.Stop()
		if err != nil {
			return err
		}
		if len(plan.GetGroups()) == 0 {
			ui.Finish("Nothing to destroy")
			return nil
		}

		consented := showDestroyPlan(ui, cfg.Slug, true, plan)
		if dry {
			ui.Diagnostic("Run without --dry to destroy.")
			return nil
		}

		granted, err := ui.ConsentByName(ctx, "project name", plan.GetSubject())
		if err != nil || !granted {
			return err
		}

		req := &contractv1.ProjectRequest{
			Slug:        cfg.Slug,
			Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW},
			Edge:        edgewire.Selection(cfg),
			Consented:   consented,
		}
		if err := provider.Stream(ctx, runner, "RemoveProject", req, contractv1connect.ProviderServiceClient.RemoveProject, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Destroyed preview footprint of project %s", cfg.Slug))
		return nil
	})
}

func showDestroyPlan(ui *runui.Session, slug string, preview bool, plan *planv1.ChangePlan) *planv1.ChangePlan {
	if preview {
		return ui.Plan(fmt.Sprintf("This will permanently destroy the ENTIRE preview footprint of project %q", slug), plan,
			"– all stored preview assets belonging to this project",
			"– every preview variable value this project holds, including each preview's own overrides",
			"The account-level preview bootstrap is left intact. This cannot be undone.")
	}
	return ui.Plan(fmt.Sprintf("This will permanently destroy production project %q", slug), plan,
		"– all stored assets belonging to this project",
		"– every production variable value this project holds, and their history",
		"This cannot be undone.")
}
