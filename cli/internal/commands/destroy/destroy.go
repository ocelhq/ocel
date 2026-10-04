package destroy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func NewCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
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
	cmd.AddCommand(newProductionCommand(invocation), newPreviewCommand(invocation))
	return commands.DeclareReadOnly(cmd)
}

func newProductionCommand(invocation commands.Invocation) *cobra.Command {
	var yes, dry bool
	cmd := &cobra.Command{
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

			return runDestroyProduction(cmd.Context(), invocation, cwd, yes, dry, cmd.OutOrStdout(), cmd.InOrStdin())
		},
	}
	commands.AddYesFlag(cmd, &yes)
	commands.AddDryFlag(cmd, &dry, "Print what would be destroyed and stop, destroying nothing")
	return commands.DeclareMutating(cmd)
}

func newPreviewCommand(invocation commands.Invocation) *cobra.Command {
	var yes, dry bool
	cmd := &cobra.Command{
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

			return runDestroyPreviewProject(cmd.Context(), invocation, cwd, yes, dry, cmd.OutOrStdout(), cmd.InOrStdin())
		},
	}
	commands.AddYesFlag(cmd, &yes)
	commands.AddDryFlag(cmd, &dry, "Print what would be destroyed and stop, destroying nothing")
	return commands.DeclareMutating(cmd)
}

func runDestroyProduction(ctx context.Context, invocation commands.Invocation, cwd string, yes, dry bool, stdout io.Writer, stdin io.Reader) error {
	cfg, err := invocation.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	bypass, notice, err := consent.Bypass{
		Noun:    "project",
		Subject: cfg.Slug,
		Action:  "destroying production",
		Verb:    "destroyed",
		Yes:     yes,
		DryRun:  dry,
		TTY:     invocation.StdinIsTerminal(stdin),
	}.Granted()
	if err != nil {
		return err
	}

	policy := consent.NewPlanPolicy("ocel destroy production", yes || bypass, invocation.StdinIsTerminal(stdin), stdout, stdin)
	policy.DryRun = dry
	policy.UnattendedRemedy = fmt.Sprintf("pass --yes, or set %s to the project name", consent.BypassEnv)
	return destroyProject(ctx, invocation, cfg, policy, environmentv1.Tier_TIER_PRODUCTION, notice)
}

func runDestroyPreviewProject(ctx context.Context, invocation commands.Invocation, cwd string, yes, dry bool, stdout io.Writer, stdin io.Reader) error {
	cfg, err := invocation.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	policy := consent.NewPlanPolicy("ocel destroy preview", yes, invocation.StdinIsTerminal(stdin), stdout, stdin)
	policy.DryRun = dry
	return destroyProject(ctx, invocation, cfg, policy, environmentv1.Tier_TIER_PREVIEW, "")
}

func destroyProject(ctx context.Context, invocation commands.Invocation, cfg *project.Project, policy consent.Policy, tier environmentv1.Tier, bypassNotice string) (err error) {
	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}
	if err := policy.Refuse(); err != nil {
		return err
	}

	ctx, run, err := invocation.Events.Begin(ctx, policy.Command, cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	if bypassNotice != "" {
		check.Warn(bypassNotice)
	}
	provider, _, err := invocation.OpenProvider(ctx, check, cfg, commands.OpenOptions{Pinning: executables.ChoosePinning(policy.DryRun), Tier: tier, Require: readiness.Features})
	check.End(err)
	if err != nil {
		return err
	}
	defer provider.Close()

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
	span := planning.Child(cfg.Slug, progress.Enumerating.Title(fmt.Sprintf("what %s has in %s to destroy", cfg.Slug, place)))
	var plan *planv1.ChangePlan
	err = provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		plan, err = client.PlanRemoveProject(ctx, &contractv1.ProjectRequest{
			Slug:        cfg.Slug,
			Environment: env,
			Edge:        cfg.EdgeSelection(),
		})
		return err
	})
	span.End(err)
	if err != nil {
		return err
	}
	if len(plan.GetGroups()) == 0 {
		run.Succeed(fmt.Sprintf("Nothing to destroy: %s has nothing in %s", cfg.Slug, place))
		return nil
	}

	consented := showDestroyPlan(planning, cfg.Slug, preview, plan)
	if policy.DryRun {
		planning.Say("Run without --dry to destroy.")
		run.Succeed(fmt.Sprintf("Planned the destroy of what %s has in %s", cfg.Slug, place))
		return nil
	}
	granted, err := policy.ConfirmPlanByName(ctx, planning, consented, "project name", plan.GetSubject())
	planning.End(err)
	if err != nil {
		return err
	}
	if !granted {
		run.Succeed(fmt.Sprintf("Nothing destroyed: what %s has in %s stays", cfg.Slug, place))
		return nil
	}

	req := &contractv1.ProjectRequest{
		Slug:        cfg.Slug,
		Environment: env,
		Edge:        cfg.EdgeSelection(),
		Consented:   consented,
	}
	if _, err := providerprocess.Stream(ctx, provider, "RemoveProject", req, contractv1connect.ProviderServiceClient.RemoveProject); err != nil {
		return err
	}
	if preview {
		run.Succeed(fmt.Sprintf("Destroyed preview footprint of project %s", cfg.Slug))
		return nil
	}
	run.Succeed(fmt.Sprintf("Destroyed project %s", cfg.Slug))
	return nil
}

func showDestroyPlan(planning *run.Span, slug string, preview bool, plan *planv1.ChangePlan) *planv1.ChangePlan {
	if preview {
		return planning.Plan(fmt.Sprintf("This will permanently destroy the ENTIRE preview footprint of project %q", slug), plan,
			&planv1.Note{Action: planv1.Change_ACTION_DELETE, Text: "all stored preview assets belonging to this project"},
			&planv1.Note{Action: planv1.Change_ACTION_DELETE, Text: "every preview variable value this project has, including each preview's own overrides"},
			&planv1.Note{Text: "The account-level preview bootstrap is left intact. This cannot be undone."})
	}
	return planning.Plan(fmt.Sprintf("This will permanently destroy production project %q", slug), plan,
		&planv1.Note{Action: planv1.Change_ACTION_DELETE, Text: "all stored assets belonging to this project"},
		&planv1.Note{Action: planv1.Change_ACTION_DELETE, Text: "every production variable value this project has, and their history"},
		&planv1.Note{Text: "This cannot be undone."})
}
