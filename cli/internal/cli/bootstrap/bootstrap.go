package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/huh/v2"
	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/changeplan"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/providerui"
	"github.com/ocelhq/ocel/cli/internal/cli/style"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
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

	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Skip confirmation")
	cmd.Flags().BoolVar(&opts.Dry, "dry", false, "Print the changes and stop, applying nothing")
	cmd.Flags().StringVar(&opts.Features, "features", "", "Comma-separated `set` of features to add or refresh; whatever else stands is left alone (also: all, none)")
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

	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Skip confirmation")
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

func Run(ctx context.Context, deps cmddeps.Deps, cwd string, tier environmentv1.Tier, opts Options, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := resolveProject(ctx, deps, cwd)
	if err != nil {
		return err
	}

	return providerui.Run(ctx, deps, cfg, "ocel bootstrap "+Name(tier), stdout, func(ctx context.Context, runner *provider.Runner, ui *deployui.Session) error {
		client, err := runner.Client()
		if err != nil {
			return err
		}
		planned, err := client.PlanBootstrap(ctx, &contractv1.PlanBootstrapRequest{
			Tier:           tier,
			WithDependents: true,
			Edge:           edgewire.Selection(cfg),
		})
		if err != nil {
			if connect.CodeOf(err) == connect.CodeUnimplemented {
				return fmt.Errorf("%s cannot say which features a bootstrap has; it predates them. Upgrade the provider pinned in this project and try again", runner.Package())
			}
			return err
		}
		catalogue := planned.GetFeatures()

		named, err := parseRemoveFlag(opts.Remove, catalogue)
		if err != nil {
			return err
		}
		standing := enabledFeatures(catalogue)
		going := goingFeatures(catalogue, standing, named)
		if absent := without(named, standing); len(absent) > 0 {
			fmt.Fprintf(stdout, "%s is not in the %s bootstrap, so there is nothing to remove.\n",
				strings.Join(absent, ", "), Name(tier))
		}
		if len(named) > 0 && len(going) == 0 && !opts.FeaturesDeclared {
			return nil
		}

		interactive := !opts.Yes && deps.StdinIsTerminal(stdin)
		picked := interactive && !opts.FeaturesDeclared
		requested, selected, err := chooseFeatures(ctx, opts, catalogue, standing, going, string(cfg.EdgeKind()), tier, interactive, stdout)
		if err != nil {
			return err
		}
		if !selected {
			fmt.Fprintln(stdout, "Aborted.")
			return nil
		}
		if err := bothWays(requested, named); err != nil {
			return err
		}

		req := &contractv1.BootstrapRequest{
			Tier:               tier,
			Features:           requested,
			Remove:             going,
			Force:              opts.Force,
			AcceptReplacements: opts.Yes,
			Edge:               edgewire.Selection(cfg),
		}
		if opts.AutoHealDeclared {
			req.AutoHeal = &opts.AutoHeal
		}

		spinner := deployui.StartSpinner(stdout, "Planning changes")
		intended, err := client.PlanBootstrap(ctx, &contractv1.PlanBootstrapRequest{
			Tier:   tier,
			Intent: req,
			Edge:   edgewire.Selection(cfg),
		})
		spinner.Stop()
		if err != nil {
			if connect.CodeOf(err) == connect.CodeUnimplemented {
				return fmt.Errorf("%s cannot say what a bootstrap would change; it predates planning. Upgrade the provider pinned in this project and try again", runner.Package())
			}
			return err
		}
		plan := intended.GetPlan()
		rendered := len(plan.GetGroups()) > 0
		if rendered {
			changeplan.NewPrinter(stdout).Render(fmt.Sprintf("Proposed changes to the %s bootstrap", Name(tier)), plan)
			if changeplan.AllKeep(plan) {
				fmt.Fprint(stdout, "\nNo infrastructure changes — applying refreshes bootstrap seals and records.\n")
			}
		} else if len(going) > 0 {
			fmt.Fprintf(stdout, "Removing %s from the %s bootstrap tears down what it stood up.\n", strings.Join(going, ", "), Name(tier))
			if dependents := dependentProjects(catalogue, going); len(dependents) > 0 {
				fmt.Fprintf(stdout, "These projects were deployed against it and break when it goes: %s\n", strings.Join(dependents, ", "))
			}
		} else {
			fmt.Fprintln(stdout, "No infrastructure changes — applying refreshes bootstrap seals and records.")
		}
		if !picked {
			kind := plan.GetEdgeKind()
			if kind == "" {
				kind = string(cfg.EdgeKind())
			}
			printImplied(stdout, impliedFeatures(catalogue, requested, kind))
		}
		status := planned.GetBootstrap()
		if status.GetDowngrade() {
			fmt.Fprintln(stdout, downgradeWarning(tier, status))
		}
		if opts.Dry {
			fmt.Fprintln(stdout, "Run without --dry to apply.")
			return nil
		}

		if interactive && status.GetDowngrade() {
			proceed, err := confirm(ctx, "Write the older content anyway?", stdin)
			if err != nil {
				return err
			}
			if !proceed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		if interactive {
			title := fmt.Sprintf("Bootstrap %s infrastructure with %s?", Name(tier), runner.Package())
			if rendered && !changeplan.AllKeep(plan) {
				title = fmt.Sprintf("%s with %s?", changeplan.ConfirmVerb(plan), runner.Package())
			}
			proceed, err := confirm(ctx, title, stdin)
			if err != nil {
				return err
			}
			if !proceed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
			if rendered {
				req.AcceptReplacements = true
			}
			req.Force = req.Force || len(going) > 0
		}

		if err := provider.Stream(ctx, runner, "Bootstrap", req, contractv1connect.ProviderServiceClient.Bootstrap, ui.Event); err != nil {
			return err
		}
		ui.Finish("Bootstrapped")
		return nil
	})
}

func resolveProject(ctx context.Context, deps cmddeps.Deps, cwd string) (*projectconfig.Config, error) {
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func confirm(ctx context.Context, title string, stdin io.Reader) (bool, error) {
	var proceed bool
	err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(title).
			Affirmative("Yes").
			Negative("No").
			Value(&proceed),
	)).WithTheme(style.Theme).WithInput(stdin).RunWithContext(ctx)
	if errors.Is(err, huh.ErrUserAborted) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return proceed, nil
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
