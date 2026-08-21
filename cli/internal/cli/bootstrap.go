package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/node"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type bootstrapOptions struct {
	yes      bool
	preview  bool
	destroy  bool
	force    bool
	features string
	declared bool
}

var bootstrapOpts bootstrapOptions

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Provision the account-global resources your provider needs",
	Long: "Provision the account-global resources your provider needs.\n\n" +
		"--features takes the whole set this class should carry, comma separated, plus " +
		"`all` and `none`; anything already there and left out of the set is removed. " +
		"Without the flag a terminal picks from a list and everything else keeps what is " +
		"already there. Removing a feature other projects deploy against needs --force.\n\n" +
		"With --destroy it removes them instead: the whole bootstrap set of that class, and the " +
		"account state, buckets, credentials and stored parameters that were built on it. It " +
		"refuses while any project or the preview wildcard is still deployed into the class, and " +
		"names what has to go first. Nothing cascades.\n\n" +
		"Removing a substrate is irreversible and requires typing the class name to confirm; " +
		"--yes skips that, as does " + destroyBypassEnv + " set to the class name.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		opts := bootstrapOpts
		opts.declared = cmd.Flags().Changed("features")

		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		return runBootstrap(ctx, defaultDeps(), cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func init() {
	bootstrapCmd.Flags().BoolVarP(&bootstrapOpts.yes, "yes", "y", false, "Skip the confirmation prompt")
	bootstrapCmd.Flags().BoolVar(&bootstrapOpts.preview, "preview", false, "Stand up the preview infrastructure instead of the production infrastructure")
	bootstrapCmd.Flags().BoolVar(&bootstrapOpts.destroy, "destroy", false, "Remove this class's infrastructure instead of provisioning it, once nothing is deployed into it")
	bootstrapCmd.Flags().StringVar(&bootstrapOpts.features, "features", "", "The whole `set` of features this class carries, comma separated; all or none for the extremes")
	bootstrapCmd.Flags().BoolVar(&bootstrapOpts.force, "force", false, "Remove a feature other projects still deploy against")
}

func runBootstrap(ctx context.Context, d deps, cwd string, opts bootstrapOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	if opts.declared && opts.destroy {
		return errors.New("--features chooses what a bootstrap carries and --destroy removes the whole substrate; pass one or the other")
	}

	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	if err := node.Ensure(cfg.Dir); err != nil {
		return err
	}

	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	tier := environmentv1.Tier_TIER_PRODUCTION
	if opts.preview {
		tier = environmentv1.Tier_TIER_PREVIEW
	}

	if opts.destroy {
		return runBootstrapDestroy(ctx, d, cfg, provider, tier, opts, stdout, stderr, stdin)
	}

	ctx, run, err := startRun(ctx, cfg, "ocel bootstrap")
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sessionFormat(), verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, d, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		client, err := runner.Deployments()
		if err != nil {
			return err
		}
		described, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{
			Tier: tier,
		})
		if err != nil {
			if connect.CodeOf(err) == connect.CodeUnimplemented {
				return fmt.Errorf("%s cannot say which features a bootstrap carries; it predates them. Upgrade the provider pinned in this project and try again", provider.Package)
			}
			return err
		}
		catalogue := described.GetFeatures()

		interactive := !opts.yes && d.stdinIsTerminal(stdin)
		requested, chosen, err := chooseFeatures(ctx, opts, catalogue, interactive, stdout, stdin)
		if err != nil {
			return err
		}
		if !chosen {
			fmt.Fprintln(stdout, "Aborted.")
			return nil
		}

		force := opts.force
		dropped := droppedFeatures(enabledFeatures(catalogue), requested)
		if len(dropped) > 0 && !force {
			if !interactive {
				return fmt.Errorf("this would remove %s from the %s bootstrap; projects deployed against it break when it goes, so re-run with --force to remove it anyway",
					strings.Join(dropped, ", "), substrateName(tier))
			}
			confirmed, err := confirmDrop(ctx, tier, dropped, dependentProjects(catalogue, dropped), stdout, stdin)
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
			force = true
		}

		if interactive {
			proceed, err := confirmBootstrap(ctx, tier, provider.Package, stdout, stdin)
			if err != nil {
				return err
			}
			if !proceed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		req := &contractv1.BootstrapRequest{
			Tier:     tier,
			Features: requested,
			Force:    force,
		}
		if err := runner.Bootstrap(ctx, req, ui.Event); err != nil {
			return err
		}
		ui.Finish("Bootstrapped")
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

func chooseFeatures(ctx context.Context, opts bootstrapOptions, catalogue []*contractv1.Feature, interactive bool, stdout io.Writer, stdin io.Reader) ([]string, bool, error) {
	if opts.declared {
		requested, err := parseFeatureFlag(opts.features, catalogue)
		return requested, err == nil, err
	}
	if !interactive {
		return enabledFeatures(catalogue), true, nil
	}
	return pickFeatures(ctx, catalogue, stdout, stdin)
}

func confirmDrop(ctx context.Context, tier environmentv1.Tier, dropped, dependents []string, stdout io.Writer, stdin io.Reader) (bool, error) {
	fmt.Fprintf(stdout, "Removing %s from the %s bootstrap tears down what it stood up.\n", strings.Join(dropped, ", "), substrateName(tier))
	if len(dependents) > 0 {
		fmt.Fprintf(stdout, "These projects were deployed against it and break when it goes: %s\n", strings.Join(dependents, ", "))
	} else {
		fmt.Fprintln(stdout, "No project deployed here has recorded needing it, but anything relying on it breaks.")
	}
	return confirmYN(ctx, "Remove it anyway?", stdout, stdin)
}

func confirmBootstrap(ctx context.Context, tier environmentv1.Tier, providerPackage string, stdout io.Writer, stdin io.Reader) (bool, error) {
	return confirmYN(ctx, fmt.Sprintf("Bootstrap %s infrastructure with %s?", substrateName(tier), providerPackage), stdout, stdin)
}

func substrateName(tier environmentv1.Tier) string {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return "preview"
	}
	return "production"
}

func runBootstrapDestroy(ctx context.Context, d deps, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor, tier environmentv1.Tier, opts bootstrapOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	substrate := substrateName(tier)
	requested := destroyBypassRequest()
	bypass := requested == substrate
	tty := d.stdinIsTerminal(stdin)
	switch {
	case bypass:
		fmt.Fprintf(stderr, "%s=%s: removing the %s substrate without confirmation\n", destroyBypassEnv, substrate, substrate)
	case requested == "" || opts.yes:
	case !tty:
		return fmt.Errorf("%s is set to %q, but this is the %s substrate; it must name the substrate being removed", destroyBypassEnv, requested, substrate)
	default:
		fmt.Fprintf(stderr, "%s is set to %q, not this substrate (%s); confirming interactively instead\n", destroyBypassEnv, requested, substrate)
	}
	skipConfirmation := opts.yes || bypass
	if !skipConfirmation && !tty {
		return fmt.Errorf("`%s` needs an interactive terminal to confirm the class name; re-run with --yes, or set %s to %q, to remove it unattended",
			bootstrapDestroyCommand(opts.preview), destroyBypassEnv, substrate)
	}

	ctx, run, err := startRun(ctx, cfg, bootstrapDestroyCommand(opts.preview))
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sessionFormat(), verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, d, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		client, err := runner.Deployments()
		if err != nil {
			return err
		}

		spinner := deployui.StartSpinner(stdout, "Enumerating what would be removed")
		plan, err := client.PlanRemoveSubstrate(ctx, &contractv1.PlanRemoveSubstrateRequest{
			Tier: tier,
			Edge: edgeSelection(cfg),
		})
		spinner.Stop()
		if err != nil {
			return err
		}
		printRemovalPlan(stdout, fmt.Sprintf("This will permanently remove the %s substrate of this account", substrate), plan,
			"Every app already deployed from it keeps running and nothing can describe, update or remove it again. This cannot be undone.")
		if !skipConfirmation {
			confirmed, err := confirmPhrase(ctx, "class name", plan.GetSubject(), stdout, stdin)
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		req := &contractv1.RemoveSubstrateRequest{
			Tier: tier,
			Edge: edgeSelection(cfg),
		}
		if err := runner.RemoveSubstrate(ctx, req, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Removed the %s substrate", substrate))
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

func bootstrapDestroyCommand(preview bool) string {
	if preview {
		return "ocel bootstrap --destroy --preview"
	}
	return "ocel bootstrap --destroy"
}
