package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
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
	providercontract "github.com/ocelhq/ocel/pkg/provider"
)

type Options struct {
	Yes    bool
	Dry    bool
	Force  bool
	Remove string
}

func NewCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap <command>",
		Short: "Set up the shared infrastructure deploys run on",
		Long: "Set up the shared infrastructure deploys run on.\n\n" +
			"`ocel bootstrap production` and `ocel bootstrap preview` show the changes and ask before applying them; " +
			"--yes applies them without asking, and --dry shows them and stops.",
		Example: "  $ ocel bootstrap production\n" +
			"  $ ocel bootstrap preview\n" +
			"  $ ocel bootstrap status",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = cmd.Help()
			return &clierror.Error{Code: clierror.CodeUsage, Cause: &exitcode.ExitError{Code: 1}}
		},
	}

	cmd.AddCommand(
		newProvisionCommand(invocation, environmentv1.Tier_TIER_PRODUCTION, []string{"prod"}),
		newProvisionCommand(invocation, environmentv1.Tier_TIER_PREVIEW, nil),
		newDestroyCommand(invocation),
		newStatusCommand(invocation),
	)

	return commands.DeclareReadOnly(cmd)
}

func newProvisionCommand(invocation commands.Invocation, tier environmentv1.Tier, aliases []string) *cobra.Command {
	var opts Options

	name := readiness.TierName(tier)
	cmd := &cobra.Command{
		Use:     name,
		Aliases: aliases,
		Short:   fmt.Sprintf("Set up or update the %s environment", name),
		Long: fmt.Sprintf("Set up or update the %s environment.\n\n", name) +
			"A bootstrap only ever builds up: each run brings its base current and adds every part a project here was refused for. " +
			"--remove takes a part down, whichever added it, and refuses while a project here uses it unless --force.\n\n" +
			"Logged in to a console from a checkout linked to it, a run asks whether that console may deploy the environment here and adds the console-trust part that lets it, then records the identity it made in the console; --remove console-trust takes that trust down.\n\n" +
			"Every run prints the changes it would make before asking; --dry prints them and stops.",
		Example: fmt.Sprintf("  $ ocel bootstrap %s\n", name) +
			fmt.Sprintf("  $ ocel bootstrap %s --remove variables-key\n", name) +
			fmt.Sprintf("  $ ocel bootstrap %s --dry", name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			return Run(cmd.Context(), invocation, cwd, tier, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}

	commands.AddYesFlag(cmd, &opts.Yes)
	commands.AddDryFlag(cmd, &opts.Dry, "Print the changes and stop, applying nothing")
	cmd.Flags().StringVar(&opts.Remove, "remove", "", "Comma-separated `set` of parts to take down; nothing goes unless it is named here")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Remove a part projects here still use")

	return commands.DeclareRunEvents(commands.DeclareMutating(cmd))
}

func newDestroyCommand(invocation commands.Invocation) *cobra.Command {
	var opts Options

	cmd := &cobra.Command{
		Use:   "destroy <production|preview>",
		Short: "Tear down an environment with nothing deployed in it",
		Long: "Tear down an environment with nothing deployed in it.\n\n" +
			"Refuses while anything is still deployed there, and lists what has to go first. " +
			"Irreversible: requires typing the environment name to confirm; --yes skips that, " +
			"and --dry prints what would go and stops.",
		Example: "  $ ocel bootstrap destroy preview\n" +
			"  $ ocel bootstrap destroy preview --dry",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tier, err := environmentArg(args)
			if err != nil {
				_ = cmd.Help()
				return err
			}

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			return RunDestroy(cmd.Context(), invocation, cwd, tier, opts, cmd.OutOrStdout(), cmd.InOrStdin())
		},
	}

	commands.AddYesFlag(cmd, &opts.Yes)
	commands.AddDryFlag(cmd, &opts.Dry, "Print what would be removed and stop, removing nothing")

	return commands.DeclareRunEvents(commands.DeclareMutating(cmd))
}

func environmentArg(args []string) (environmentv1.Tier, error) {
	if len(args) == 0 {
		return environmentv1.Tier_TIER_UNSPECIFIED, errors.New("name the environment to tear down, production or preview")
	}
	return tierNamed(args[0], "tear down")
}

func tierNamed(name, purpose string) (environmentv1.Tier, error) {
	switch name {
	case "preview":
		return environmentv1.Tier_TIER_PREVIEW, nil
	case "production", "prod":
		return environmentv1.Tier_TIER_PRODUCTION, nil
	}
	return environmentv1.Tier_TIER_UNSPECIFIED, fmt.Errorf("the environment to %s is production or preview, not %q", purpose, name)
}

func Run(ctx context.Context, invocation commands.Invocation, cwd string, tier environmentv1.Tier, opts Options, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := resolveProject(ctx, invocation, cwd)
	if err != nil {
		return err
	}

	command := readiness.BootstrapCommand(tier)
	policy := invocation.PlanPolicy(command, opts.Yes, stdout, stdin)
	policy.DryRun = opts.Dry
	if err := policy.Refuse(); err != nil {
		return err
	}

	return invocation.WithProvider(ctx, cfg, command, commands.OpenOptions{Intent: commands.ManageBootstrap, Tier: tier, Policy: policy, DryRun: policy.DryRun}, func(ctx context.Context, p commands.ProviderRun) error {
		run, check, provider := p.Run, p.Check, p.Provider
		planned, err := describeBootstrap(ctx, check, provider, cfg, tier)
		check.End(err)
		if err != nil {
			return err
		}
		named := parseRemoveFlag(opts.Remove)
		parts := planned.GetParts()
		going := installedOf(parts, named)
		planning := run.Phase(progressv1.Phase_PHASE_PLAN)
		req := &contractv1.BootstrapRequest{
			Tier:   tier,
			Remove: named,
			Force:  opts.Force,
			Edge:   cfg.EdgeSelection(),
		}
		if len(named) > 0 && len(going) == 0 {
			if err := refuseUnofferedParts(ctx, planning, provider, req); err != nil {
				return err
			}
			sayNothingToRemove(planning, tier, named)
			return nil
		}

		recorded := planned.GetBootstrap().GetTrust()
		trusted, err := readConsoleTrust(ctx, invocation, p, tier, policy, named, recorded, planning)
		if err != nil {
			return err
		}
		if trusted != nil {
			req.Trust = trusted.trust
		}

		drawn, err := drawPlan(ctx, planning, provider, req)
		if err != nil {
			return err
		}
		sayNothingToRemove(planning, tier, without(named, going))
		switch {
		case drawn.rendered():
		case len(going) > 0:
			planning.Warn(fmt.Sprintf("Removing %s from the %s bootstrap tears down what it installed.", strings.Join(going, ", "), readiness.TierName(tier)))
			if dependents := usersOf(parts, going); len(dependents) > 0 {
				planning.Warn(fmt.Sprintf("These projects were deployed against it and break when it goes: %s", strings.Join(dependents, ", ")))
			}
		default:
			planning.Say(unchanged(tier))
		}
		status := planned.GetBootstrap()
		if status.GetDowngrade() {
			planning.Warn(downgradeWarning(tier, status))
		}
		if opts.Dry {
			planning.Say("Run without --dry to apply.")
			run.Succeed(fmt.Sprintf("Planned the %s bootstrap", readiness.TierName(tier)))
			return nil
		}

		if status.GetDowngrade() {
			proceed, err := policy.Confirm(ctx, planning, consent.Guard{
				ID:       consent.GuardBootstrapDowngrade,
				Question: "Write the older content anyway?",
				Action:   fmt.Sprintf("writing older %s bootstrap content", readiness.TierName(tier)),
			})
			if err != nil {
				return err
			}
			if !proceed {
				run.Succeed(fmt.Sprintf("Left the %s bootstrap as it is", readiness.TierName(tier)))
				return nil
			}
		}

		granted, err := confirmPlan(ctx, policy, planning, provider, tier, drawn)
		if err != nil {
			return err
		}
		if !granted {
			run.Succeed(fmt.Sprintf("Left the %s bootstrap as it is", readiness.TierName(tier)))
			return nil
		}
		if slices.Contains(going, providercontract.ConsoleTrustPart) {
			if err := forgetConsoleTrust(ctx, invocation, recorded, tier, policy, planning); err != nil {
				return err
			}
		}
		planning.End(nil)

		if err := applyPlan(ctx, provider, req, drawn); err != nil {
			return err
		}
		if trusted != nil {
			recording := run.Phase(progressv1.Phase_PHASE_PROVISION)
			err := trusted.record(ctx, recording, p, tier)
			recording.End(err)
			if err != nil {
				return err
			}
		}
		run.Succeed(fmt.Sprintf("Bootstrapped the %s environment", readiness.TierName(tier)))
		return nil
	})
}

type drawnPlan struct {
	shown *planv1.ChangePlan
}

func (d drawnPlan) rendered() bool { return len(d.shown.GetGroups()) > 0 }

func drawPlan(ctx context.Context, planning *run.Span, provider *providerprocess.Provider, req *contractv1.BootstrapRequest) (drawnPlan, error) {
	tier := readiness.TierName(req.GetTier())
	plan, err := planChanges(ctx, planning, provider, req)
	if err != nil {
		return drawnPlan{}, err
	}
	if len(plan.GetGroups()) == 0 {
		return drawnPlan{}, nil
	}
	var notes []*planv1.Note
	if !consent.Mutates(plan) {
		notes = append(notes, &planv1.Note{Text: unchanged(req.GetTier())})
	}
	return drawnPlan{shown: planning.Plan(fmt.Sprintf("Proposed changes to the %s bootstrap", tier), plan, notes...)}, nil
}

func planChanges(ctx context.Context, planning *run.Span, provider *providerprocess.Provider, req *contractv1.BootstrapRequest) (*planv1.ChangePlan, error) {
	tier := readiness.TierName(req.GetTier())
	dry := proto.CloneOf(req)
	dry.Dry = true
	spanCtx, span := planning.Child(ctx, tier, progress.Planning.Title(fmt.Sprintf("the changes to the %s bootstrap", tier)))
	plan, err := providerprocess.Plan(spanCtx, provider, "Bootstrap", dry, contractv1connect.ProviderServiceClient.Bootstrap)
	span.End(err)
	return plan, err
}

func refuseUnofferedParts(ctx context.Context, planning *run.Span, provider *providerprocess.Provider, req *contractv1.BootstrapRequest) error {
	_, err := planChanges(ctx, planning, provider, req)
	return err
}

func sayNothingToRemove(planning *run.Span, tier environmentv1.Tier, absent []string) {
	if len(absent) > 0 {
		planning.Say(fmt.Sprintf("%s is not in the %s bootstrap, so there is nothing to remove.", strings.Join(absent, ", "), readiness.TierName(tier)))
	}
}

func confirmPlan(ctx context.Context, policy consent.Policy, planning *run.Span, provider *providerprocess.Provider, tier environmentv1.Tier, drawn drawnPlan) (bool, error) {
	title := fmt.Sprintf("Bootstrap %s infrastructure with %s?", readiness.TierName(tier), provider.Name())
	if drawn.rendered() {
		title = fmt.Sprintf("%s with %s?", consent.ConfirmVerb(drawn.shown), provider.Name())
	}
	return policy.ConfirmPlan(ctx, planning, drawn.shown, title)
}

func applyPlan(ctx context.Context, provider *providerprocess.Provider, req *contractv1.BootstrapRequest, drawn drawnPlan) error {
	req.Consented = drawn.shown
	req.AcceptReplacements = drawn.rendered()
	_, err := providerprocess.Stream(ctx, provider, "Bootstrap", req, contractv1connect.ProviderServiceClient.Bootstrap)
	return err
}

func describeBootstrap(ctx context.Context, check *run.Span, provider *providerprocess.Provider, cfg *project.Project, tier environmentv1.Tier) (*contractv1.DescribeBootstrapResponse, error) {
	name := readiness.TierName(tier)
	ctx, span := check.Child(ctx, name, progress.Reading.Title("the parts of the "+name+" bootstrap"))
	var planned *contractv1.DescribeBootstrapResponse
	err := provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		planned, err = client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{
			Tier:      tier,
			WithParts: true,
			Edge:      cfg.EdgeSelection(),
		})
		return err
	})
	span.End(err)
	return planned, err
}

func resolveProject(ctx context.Context, invocation commands.Invocation, cwd string) (*project.Project, error) {
	cfg, err := invocation.LoadProject(ctx, cwd)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func downgradeWarning(tier environmentv1.Tier, status *contractv1.BootstrapStatus) string {
	var wroteIt string
	for _, stack := range status.GetStacks() {
		if stack.GetPart() == "" {
			wroteIt = stack.GetWrittenBy()
		}
	}
	return fmt.Sprintf(
		"The %s bootstrap was last written by %s and this one is %s: the same shape, older content.\nEvery stack it writes goes back to what this build has.",
		readiness.TierName(tier), wroteIt, status.GetWriter(),
	)
}

func unchanged(tier environmentv1.Tier) string {
	return fmt.Sprintf("Nothing in the %s bootstrap's infrastructure changes: applying only refreshes its seals and records", readiness.TierName(tier))
}
