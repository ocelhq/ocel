package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/preflight"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
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
	AutoHealDeclared bool
	AutoHeal         bool
}

func NewCommand(deps cmddeps.Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap <command>",
		Short: "Set up the shared infrastructure deploys run on",
		Long:  "Set up the shared infrastructure deploys run on.",
		Example: "  $ ocel bootstrap production\n" +
			"  $ ocel bootstrap preview --features all",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = cmd.Help()
			return &exitsig.ExitError{Code: 1}
		},
	}

	cmd.AddCommand(
		newProvisionCommand(deps, environmentv1.Tier_TIER_PRODUCTION, []string{"prod"}),
		newProvisionCommand(deps, environmentv1.Tier_TIER_PREVIEW, nil),
		newDestroyCommand(deps),
	)

	return cmd
}

func newProvisionCommand(deps cmddeps.Deps, tier environmentv1.Tier, aliases []string) *cobra.Command {
	var opts Options

	name := Name(tier)
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
			fmt.Sprintf("  $ ocel bootstrap %s --auto-heal", name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			opts := opts
			opts.FeaturesDeclared = cmd.Flags().Changed("features")
			opts.AutoHealDeclared = cmd.Flags().Changed("auto-heal")

			ctx, stop := deps.Interrupt(cmd.Context(), cmd.ErrOrStderr())
			defer stop()

			return Run(ctx, deps, cwd, tier, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}

	cmddeps.Yes(cmd, &opts.Yes)
	cmd.Flags().BoolVar(&opts.Dry, "dry", false, "Print the changes and stop, applying nothing")
	cmd.Flags().StringVar(&opts.Features, "features", "", "Comma-separated `set` of features to add or refresh; whatever else is installed is left alone (also: all, none)")
	cmd.Flags().StringVar(&opts.Remove, "remove", "", "Comma-separated `set` of features to tear down; nothing goes unless it is named here")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Remove a feature other projects still use")
	cmd.Flags().BoolVar(&opts.AutoHeal, "auto-heal", false, "Let later deploys refresh stale features on their own; --auto-heal=false turns it off")

	return cmd
}

func newDestroyCommand(deps cmddeps.Deps) *cobra.Command {
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

			ctx, stop := deps.Interrupt(cmd.Context(), cmd.ErrOrStderr())
			defer stop()

			return RunDestroy(ctx, deps, cwd, tier, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}

	cmddeps.Yes(cmd, &opts.Yes)
	cmd.Flags().BoolVar(&opts.Dry, "dry", false, "Print what would be removed and stop, removing nothing")

	return cmd
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

func Run(ctx context.Context, deps cmddeps.Deps, cwd string, tier environmentv1.Tier, opts Options, stdout, stderr io.Writer, stdin io.Reader) (err error) {
	cfg, err := resolveProject(ctx, deps, cwd)
	if err != nil {
		return err
	}

	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}
	command := "ocel bootstrap " + Name(tier)
	gate := deps.Gate(consent.PlanFirst, command, opts.Yes, stdout, stdin)
	gate.Dry = opts.Dry
	gate.Unattended = "pass --yes"
	if err := gate.Refuse(); err != nil {
		return err
	}

	ctx, run, err := deps.Events.Begin(ctx, command, cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := providerclient.Start(ctx, cfg, check, deps.HostTrust, providerclient.ChoosePinning(opts.Dry))
	if err != nil {
		return err
	}
	defer prov.Close()

	planned, err := describeBootstrap(ctx, check, prov, cfg, tier)
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
	if absent := without(named, installed); len(absent) > 0 {
		fmt.Fprintf(stdout, "%s is not in the %s bootstrap, so there is nothing to remove.\n",
			strings.Join(absent, ", "), Name(tier))
	}
	if len(named) > 0 && len(going) == 0 && !opts.FeaturesDeclared {
		return nil
	}

	planning := run.Phase(progressv1.Phase_PHASE_PLAN)
	asking := gate.Asking()
	picked := asking && !opts.FeaturesDeclared
	requested, selected, err := chooseFeatures(ctx, planning, opts, catalogue, installed, going, string(cfg.EdgeID()), tier, asking, stdout)
	if err != nil {
		return err
	}
	if !selected {
		fmt.Fprintln(stdout, "Aborted.")
		run.Finish("Nothing bootstrapped")
		return nil
	}
	if err := bothWays(requested, named); err != nil {
		return err
	}

	request := func(dry bool) *contractv1.BootstrapRequest {
		req := &contractv1.BootstrapRequest{
			Tier:     tier,
			Features: requested,
			Remove:   going,
			Force:    opts.Force,
			Edge:     edgewire.Selection(cfg),
			Dry:      dry,
		}
		if opts.AutoHealDeclared {
			req.AutoHeal = &opts.AutoHeal
		}
		return req
	}

	unit := planning.Unit(Name(tier), "Planning changes")
	plan, err := providerclient.Plan(ctx, prov, "Bootstrap", request(true), contractv1connect.ProviderServiceClient.Bootstrap)
	unit.End(err)
	if err != nil {
		return err
	}
	var consented *planv1.ChangePlan
	rendered := len(plan.GetGroups()) > 0
	switch {
	case rendered:
		var notes []string
		if !consent.Mutates(plan) {
			notes = append(notes, "No infrastructure changes — applying refreshes bootstrap seals and records.")
		}
		consented = planning.Plan(fmt.Sprintf("Proposed changes to the %s bootstrap", Name(tier)), plan, notes...)
	case len(going) > 0:
		planning.Warn(fmt.Sprintf("Removing %s from the %s bootstrap tears down what it installed.", strings.Join(going, ", "), Name(tier)))
		if dependents := dependentProjects(catalogue, going); len(dependents) > 0 {
			planning.Warn(fmt.Sprintf("These projects were deployed against it and break when it goes: %s", strings.Join(dependents, ", ")))
		}
	default:
		planning.Say("No infrastructure changes — applying refreshes bootstrap seals and records.")
	}
	if !picked {
		edgeID := plan.GetEdgeKind()
		if edgeID == "" {
			edgeID = string(cfg.EdgeID())
		}
		printImplied(stdout, impliedFeatures(catalogue, requested, edgeID))
	}
	status := planned.GetBootstrap()
	if status.GetDowngrade() {
		fmt.Fprintln(stdout, downgradeWarning(tier, status))
	}
	if opts.Dry {
		planning.Say("Run without --dry to apply.")
		return nil
	}

	if status.GetDowngrade() {
		proceed, err := gate.Guard(ctx, planning, "Write the older content anyway?")
		if err != nil {
			return err
		}
		if !proceed {
			run.Finish("Nothing bootstrapped")
			return nil
		}
	}

	title := fmt.Sprintf("Bootstrap %s infrastructure with %s?", Name(tier), prov.Name())
	if rendered {
		title = fmt.Sprintf("%s with %s?", consent.ConfirmVerb(consented), prov.Name())
	}
	granted, err := gate.Consent(ctx, planning, consented, title)
	if err != nil {
		return err
	}
	if !granted {
		run.Finish("Nothing bootstrapped")
		return nil
	}
	planning.End(nil)

	req := request(false)
	req.Consented = consented
	req.AcceptReplacements = rendered
	req.Force = req.Force || len(going) > 0

	if _, err := providerclient.Stream(ctx, prov, "Bootstrap", req, contractv1connect.ProviderServiceClient.Bootstrap); err != nil {
		return err
	}
	run.Finish("Bootstrapped")
	return nil
}

func describeBootstrap(ctx context.Context, check *events.Scope, prov *providerclient.Provider, cfg *projectconfig.Config, tier environmentv1.Tier) (*contractv1.DescribeBootstrapResponse, error) {
	if err := preflight.Announce(ctx, check, prov, cfg, tier); err != nil {
		return nil, err
	}
	var planned *contractv1.DescribeBootstrapResponse
	err := prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		planned, err = client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{
			Tier:           tier,
			WithDependents: true,
			Edge:           edgewire.Selection(cfg),
		})
		return err
	})
	if connect.CodeOf(err) == connect.CodeUnimplemented {
		return nil, fmt.Errorf("%s cannot say which features a bootstrap has; it predates them. Upgrade the provider pinned in this project and try again", prov.Name())
	}
	return planned, err
}

func resolveProject(ctx context.Context, deps cmddeps.Deps, cwd string) (*projectconfig.Config, error) {
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
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
		Name(tier), wroteIt, status.GetWriter(),
	)
}

func Name(tier environmentv1.Tier) string {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return "preview"
	}
	return "production"
}
