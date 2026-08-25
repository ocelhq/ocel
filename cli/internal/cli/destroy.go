package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/changeplan"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/providerui"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/prompt"
	"github.com/ocelhq/ocel/cli/internal/provider"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
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
		"An automated caller that must tear its own project down unattended can set " +
		changeplan.BypassEnv + " to the project name — and only that name — to skip both gates. " +
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

		return runDestroyProduction(ctx, newDeps(), cwd, destroyProductionDry, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

var (
	destroyYes           bool
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

			return runDestroyPreviewProject(ctx, newDeps(), cwd, destroyYes, destroyPreviewDry, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}
	previewCmd.Flags().BoolVarP(&destroyYes, "yes", "y", false, "Destroy the whole preview footprint with no confirmation and no terminal, for CI. Skips both the typed-name confirmation and the interactive-terminal requirement")
	previewCmd.Flags().BoolVar(&destroyPreviewDry, "dry", false, "Print what would be destroyed and stop, destroying nothing")
	destroyProductionCmd.Flags().BoolVar(&destroyProductionDry, "dry", false, "Print what would be destroyed and stop, destroying nothing")

	destroyCmd.AddCommand(destroyProductionCmd, previewCmd)
	rootCmd.AddCommand(destroyCmd)
}

func runDestroyProduction(ctx context.Context, deps cmddeps.Deps, cwd string, dry bool, stdout, stderr io.Writer, stdin io.Reader) error {
	requested := changeplan.BypassRequest()
	tty := isReaderTTY(stdin)
	if !dry && requested == "" && !tty {
		return fmt.Errorf("`ocel destroy production` needs an interactive terminal to confirm the project name; to destroy unattended, set %s to the project name", changeplan.BypassEnv)
	}

	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	bypass := !dry && requested == cfg.Slug
	switch {
	case dry:
	case bypass:
		fmt.Fprintf(stderr, "%s=%s: destroying production without confirmation\n", changeplan.BypassEnv, cfg.Slug)
	case requested != "" && !tty:
		return fmt.Errorf("%s is set to %q, but this project is %q; it must name the project being destroyed", changeplan.BypassEnv, requested, cfg.Slug)
	case requested != "":
		fmt.Fprintf(stderr, "%s is set to %q, not this project (%s); confirming interactively instead\n", changeplan.BypassEnv, requested, cfg.Slug)
	}

	return providerui.Run(ctx, deps, cfg, "ocel destroy production", stdout, func(ctx context.Context, runner *provider.Runner, ui *deployui.Session) error {
		if err := preflightTier(ctx, deps, runner, cfg, environmentv1.Tier_TIER_PRODUCTION, "ocel bootstrap production", stdout); err != nil {
			return err
		}

		client, err := runner.Client()
		if err != nil {
			return err
		}

		spinner := deployui.StartSpinner(stdout, "Enumerating what would be destroyed")
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

		printDestroyPlan(stdout, cfg.Slug, false, plan)
		if dry {
			fmt.Fprintln(stdout, "Run without --dry to destroy.")
			return nil
		}
		if !bypass {
			confirmed, err := prompt.New(stdout, stdin).Phrase(ctx, "project name", plan.GetSubject())
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		req := &contractv1.ProjectRequest{
			Slug: cfg.Slug,
			Edge: edgewire.Selection(cfg),
		}
		if err := provider.Stream(ctx, runner, "RemoveProject", req, contractv1connect.ProviderServiceClient.RemoveProject, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Destroyed project %s", cfg.Slug))
		return nil
	})
}

func runDestroyPreviewProject(ctx context.Context, deps cmddeps.Deps, cwd string, yes, dry bool, stdout, stderr io.Writer, stdin io.Reader) error {
	if !dry && !yes && !isReaderTTY(stdin) {
		return errors.New("`ocel destroy preview` needs an interactive terminal to confirm the project name; re-run with --yes to tear the preview footprint down non-interactively")
	}

	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	return providerui.Run(ctx, deps, cfg, "ocel destroy preview", stdout, func(ctx context.Context, runner *provider.Runner, ui *deployui.Session) error {
		if err := preflightTier(ctx, deps, runner, cfg, environmentv1.Tier_TIER_PREVIEW, "ocel bootstrap preview", stdout); err != nil {
			return err
		}

		client, err := runner.Client()
		if err != nil {
			return err
		}

		spinner := deployui.StartSpinner(stdout, "Enumerating what would be destroyed")
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

		printDestroyPlan(stdout, cfg.Slug, true, plan)
		if dry {
			fmt.Fprintln(stdout, "Run without --dry to destroy.")
			return nil
		}

		if !yes {
			confirmed, err := prompt.New(stdout, stdin).Phrase(ctx, "project name", plan.GetSubject())
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		req := &contractv1.ProjectRequest{
			Slug:        cfg.Slug,
			Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW},
			Edge:        edgewire.Selection(cfg),
		}
		if err := provider.Stream(ctx, runner, "RemoveProject", req, contractv1connect.ProviderServiceClient.RemoveProject, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Destroyed preview footprint of project %s", cfg.Slug))
		return nil
	})
}

func printDestroyPlan(out io.Writer, slug string, preview bool, plan *contractv1.ChangePlan) {
	if preview {
		changeplan.NewPrinter(out).Print(fmt.Sprintf("This will permanently destroy the ENTIRE preview footprint of project %q", slug), plan,
			"– all stored preview assets belonging to this project",
			"– every preview variable value this project holds, including each preview's own overrides",
			"The account-level preview bootstrap is left intact. This cannot be undone.")
		return
	}
	changeplan.NewPrinter(out).Print(fmt.Sprintf("This will permanently destroy production project %q", slug), plan,
		"– all stored assets belonging to this project",
		"– every production variable value this project holds, and their history",
		"This cannot be undone.")
}
