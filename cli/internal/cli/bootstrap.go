package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/node"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type bootstrapOptions struct {
	yes     bool
	preview bool
	destroy bool
}

var bootstrapOpts bootstrapOptions

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Provision the account-global resources your provider needs",
	Long: "Provision the account-global resources your provider needs.\n\n" +
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

		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		return runBootstrap(ctx, defaultDeps(), cwd, bootstrapOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func init() {
	bootstrapCmd.Flags().BoolVarP(&bootstrapOpts.yes, "yes", "y", false, "Skip the confirmation prompt")
	bootstrapCmd.Flags().BoolVar(&bootstrapOpts.preview, "preview", false, "Stand up the preview infrastructure instead of the production infrastructure")
	bootstrapCmd.Flags().BoolVar(&bootstrapOpts.destroy, "destroy", false, "Remove this class's infrastructure instead of provisioning it, once nothing is deployed into it")
}

func runBootstrap(ctx context.Context, d deps, cwd string, opts bootstrapOptions, stdout, stderr io.Writer, stdin io.Reader) error {
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

	class := deploymentsv1.Environment_CLASS_PRODUCTION
	if opts.preview {
		class = deploymentsv1.Environment_CLASS_PREVIEW
	}

	if opts.destroy {
		return runBootstrapDestroy(ctx, d, cfg, provider, class, opts, stdout, stderr, stdin)
	}

	if !opts.yes && d.stdinIsTerminal(stdin) {
		proceed, err := confirmBootstrap(ctx, class, provider.Package, stdout, stdin)
		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(stdout, "Interrupted.")
				return &ExitError{Code: interruptExitCode}
			}
			return err
		}
		if !proceed {
			fmt.Fprintln(stdout, "Aborted.")
			return nil
		}
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
		req := edgeSettings(cfg).applyToBootstrap(&deploymentsv1.BootstrapRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Class:           class,
		})
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

func confirmBootstrap(ctx context.Context, class deploymentsv1.Environment_Class, providerPackage string, stdout io.Writer, stdin io.Reader) (bool, error) {
	infra := "production"
	if class == deploymentsv1.Environment_CLASS_PREVIEW {
		infra = "preview"
	}
	return confirmYN(ctx, fmt.Sprintf("Bootstrap %s infrastructure with %s?", infra, providerPackage), stdout, stdin)
}

func substrateName(class deploymentsv1.Environment_Class) string {
	if class == deploymentsv1.Environment_CLASS_PREVIEW {
		return "preview"
	}
	return "production"
}

func runBootstrapDestroy(ctx context.Context, d deps, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor, class deploymentsv1.Environment_Class, opts bootstrapOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	substrate := substrateName(class)
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
		plan, err := client.PlanTeardown(ctx, edgeSettings(cfg).applyToPlanTeardown(&deploymentsv1.PlanTeardownRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Class:           class,
		}))
		spinner.Stop()
		if err != nil {
			return err
		}
		printTeardownPlan(stdout, substrate, plan)
		if !skipConfirmation {
			confirmed, err := confirmPhrase(ctx, "class name", substrate, stdout, stdin)
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		req := edgeSettings(cfg).applyToTeardown(&deploymentsv1.TeardownRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Class:           class,
		})
		if err := runner.Teardown(ctx, req, ui.Event); err != nil {
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

func printTeardownPlan(out io.Writer, substrate string, plan *deploymentsv1.PlanTeardownResponse) {
	header := fmt.Sprintf("This will permanently remove the %s substrate of this account", substrate)
	if kind := plan.GetEdgeKind(); kind != "" {
		header += fmt.Sprintf(", fronted by the %s edge", kind)
	}
	fmt.Fprintf(out, "%s:\n", header)

	var kept []*deploymentsv1.TeardownItem
	for _, item := range plan.GetItems() {
		if item.GetAction() == deploymentsv1.TeardownItem_ACTION_KEEP {
			kept = append(kept, item)
			continue
		}
		fmt.Fprintf(out, "  • %s\n", teardownItemLine(item))
	}
	fmt.Fprintln(out, "Every app already deployed from it keeps running and nothing can describe, update or remove it again. This cannot be undone.")

	if len(kept) > 0 {
		fmt.Fprintln(out, "Left in place:")
		for _, item := range kept {
			fmt.Fprintf(out, "  • %s\n", teardownItemLine(item))
		}
	}
}
