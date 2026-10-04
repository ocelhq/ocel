package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/executables"
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
)

type Options struct {
	Yes              bool
	Dry              bool
	Force            bool
	Features         string
	Remove           string
	FeaturesDeclared bool
	RepairDeclared   bool
	Repair           bool
}

func NewCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap <command>",
		Short: "Set up the shared infrastructure deploys run on",
		Long:  "Set up the shared infrastructure deploys run on.",
		Example: "  $ ocel bootstrap production\n" +
			"  $ ocel bootstrap preview --features all",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = cmd.Help()
			return &exitcode.ExitError{Code: 1}
		},
	}

	cmd.AddCommand(
		newProvisionCommand(invocation, environmentv1.Tier_TIER_PRODUCTION, []string{"prod"}),
		newProvisionCommand(invocation, environmentv1.Tier_TIER_PREVIEW, nil),
		newDestroyCommand(invocation),
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
			"A bootstrap only ever builds up: --features and the interactive picker add and refresh, " +
			"and a feature they leave out stays included. --remove is the only way anything goes.\n\n" +
			"Every run prints the changes it would make before asking; --dry prints them and stops.",
		Example: fmt.Sprintf("  $ ocel bootstrap %s\n", name) +
			fmt.Sprintf("  $ ocel bootstrap %s --features core,queues\n", name) +
			fmt.Sprintf("  $ ocel bootstrap %s --remove queues\n", name) +
			fmt.Sprintf("  $ ocel bootstrap %s --dry\n", name) +
			fmt.Sprintf("  $ ocel bootstrap %s --repair", name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			opts := opts
			opts.FeaturesDeclared = cmd.Flags().Changed("features")
			opts.RepairDeclared = cmd.Flags().Changed("repair")

			return Run(cmd.Context(), invocation, cwd, tier, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}

	commands.AddYesFlag(cmd, &opts.Yes)
	cmd.Flags().BoolVar(&opts.Dry, "dry", false, "Print the changes and stop, applying nothing")
	cmd.Flags().StringVar(&opts.Features, "features", "", "Comma-separated `set` of features to add or refresh; whatever else is installed is left alone (also: all, none)")
	cmd.Flags().StringVar(&opts.Remove, "remove", "", "Comma-separated `set` of features to tear down; nothing goes unless it is named here")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Remove a feature other projects still use")
	cmd.Flags().BoolVar(&opts.Repair, "repair", false, "Let later deploys refresh stale features on their own; --repair=false turns it off")

	return commands.DeclareMutating(cmd)
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
	cmd.Flags().BoolVar(&opts.Dry, "dry", false, "Print what would be removed and stop, removing nothing")

	return commands.DeclareMutating(cmd)
}

func environmentArg(args []string) (environmentv1.Tier, error) {
	if len(args) == 0 {
		return environmentv1.Tier_TIER_UNSPECIFIED, errors.New("name the environment to tear down, production or preview")
	}
	switch args[0] {
	case "preview":
		return environmentv1.Tier_TIER_PREVIEW, nil
	case "production", "prod":
		return environmentv1.Tier_TIER_PRODUCTION, nil
	default:
		return environmentv1.Tier_TIER_UNSPECIFIED,
			fmt.Errorf("the environment to tear down is production or preview, not %q", args[0])
	}
}

func Run(ctx context.Context, invocation commands.Invocation, cwd string, tier environmentv1.Tier, opts Options, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := resolveProject(ctx, invocation, cwd)
	if err != nil {
		return err
	}

	command := readiness.BootstrapCommand(tier)
	policy := consent.NewPlanPolicy(command, opts.Yes, invocation.StdinIsTerminal(stdin), stdout, stdin)
	policy.DryRun = opts.Dry
	if err := policy.Refuse(); err != nil {
		return err
	}

	return invocation.WithProvider(ctx, cfg, command, commands.OpenOptions{
		Pinning: executables.ChoosePinning(opts.Dry),
		Tier:    tier,
		Require: readiness.Credentials,
		Slug:    cfg.Slug,
	}, func(ctx context.Context, p commands.ProviderRun) error {
		run, check, provider := p.Run, p.Check, p.Provider
		planned, err := describeBootstrap(ctx, provider, cfg, tier)
		check.End(err)
		if err != nil {
			return err
		}
		catalogue := planned.GetFeatures()

		named, err := parseRemoveFlag(opts.Remove, catalogue)
		if err != nil {
			return err
		}
		installed := enabledFeatures(catalogue)
		going := goingFeatures(catalogue, installed, named)
		planning := run.Phase(progressv1.Phase_PHASE_PLAN)
		if absent := without(named, installed); len(absent) > 0 {
			planning.Say(fmt.Sprintf("%s is not in the %s bootstrap, so there is nothing to remove.", strings.Join(absent, ", "), readiness.TierName(tier)))
		}
		if len(named) > 0 && len(going) == 0 && !opts.FeaturesDeclared {
			return nil
		}

		asking := policy.IsAsking()
		picked := asking && !opts.FeaturesDeclared
		requested, selected, err := chooseFeatures(ctx, planning, opts, catalogue, installed, going, string(cfg.EdgeKind()), tier, asking, stdout, stdin)
		if err != nil {
			return err
		}
		if !selected {
			planning.Say("No feature was picked")
			run.Succeed(fmt.Sprintf("Left the %s bootstrap as it is", readiness.TierName(tier)))
			return nil
		}
		if err := refuseFeaturesNamedBothWays(requested, named); err != nil {
			return err
		}

		req := &contractv1.BootstrapRequest{
			Tier:     tier,
			Features: requested,
			Remove:   going,
			Force:    opts.Force,
			Edge:     cfg.EdgeSelection(),
		}
		if opts.RepairDeclared {
			req.RepairOnDeploy = &opts.Repair
		}

		drawn, err := drawPlan(ctx, planning, provider, req)
		if err != nil {
			return err
		}
		switch {
		case drawn.rendered():
		case len(going) > 0:
			planning.Warn(fmt.Sprintf("Removing %s from the %s bootstrap tears down what it installed.", strings.Join(going, ", "), readiness.TierName(tier)))
			if dependents := dependentProjects(catalogue, going); len(dependents) > 0 {
				planning.Warn(fmt.Sprintf("These projects were deployed against it and break when it goes: %s", strings.Join(dependents, ", ")))
			}
		default:
			planning.Say(unchanged(tier))
		}
		if !picked {
			edgeID := drawn.edgeKind
			if edgeID == "" {
				edgeID = string(cfg.EdgeKind())
			}
			sayImplied(planning, tier, impliedFeatures(catalogue, requested, edgeID))
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
			proceed, err := policy.Confirm(ctx, planning, "Write the older content anyway?")
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
		planning.End(nil)

		req.Force = req.Force || len(going) > 0
		if err := applyPlan(ctx, provider, req, drawn); err != nil {
			return err
		}
		run.Succeed(fmt.Sprintf("Bootstrapped the %s environment", readiness.TierName(tier)))
		return nil
	})
}

type drawnPlan struct {
	shown    *planv1.ChangePlan
	edgeKind string
}

func (d drawnPlan) rendered() bool { return len(d.shown.GetGroups()) > 0 }

func drawPlan(ctx context.Context, planning *run.Span, provider *providerprocess.Provider, req *contractv1.BootstrapRequest) (drawnPlan, error) {
	tier := readiness.TierName(req.GetTier())
	dry := proto.CloneOf(req)
	dry.Dry = true
	span := planning.Child(tier, progress.Planning.Title(fmt.Sprintf("the changes to the %s bootstrap", tier)))
	plan, err := providerprocess.Plan(ctx, provider, "Bootstrap", dry, contractv1connect.ProviderServiceClient.Bootstrap)
	span.End(err)
	if err != nil {
		return drawnPlan{}, err
	}
	drawn := drawnPlan{edgeKind: plan.GetEdgeKind()}
	if len(plan.GetGroups()) == 0 {
		return drawn, nil
	}
	var notes []*planv1.Note
	if !consent.Mutates(plan) {
		notes = append(notes, &planv1.Note{Text: unchanged(req.GetTier())})
	}
	drawn.shown = planning.Plan(fmt.Sprintf("Proposed changes to the %s bootstrap", tier), plan, notes...)
	return drawn, nil
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

func describeBootstrap(ctx context.Context, provider *providerprocess.Provider, cfg *project.Project, tier environmentv1.Tier) (*contractv1.DescribeBootstrapResponse, error) {
	var planned *contractv1.DescribeBootstrapResponse
	err := provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		planned, err = client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{
			Tier:           tier,
			WithDependents: true,
			Edge:           cfg.EdgeSelection(),
		})
		return err
	})
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
		if stack.GetFeature() == "" {
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
