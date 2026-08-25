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

	"github.com/ocelhq/ocel/cli/internal/cli/providerui"
	"github.com/ocelhq/ocel/cli/internal/cli/session"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/prompt"
	"github.com/ocelhq/ocel/cli/internal/provider"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type Options struct {
	Yes              bool
	Force            bool
	Features         string
	FeaturesDeclared bool
	AutoHealDeclared bool
	AutoHeal         bool
}

func NewCommand(sess session.Session) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap <command>",
		Short: "Set up the shared infrastructure deploys run on",
		Long:  "Set up the shared infrastructure deploys run on.",
		Example: "  $ ocel bootstrap production\n" +
			"  $ ocel bootstrap preview --features all\n" +
			"  $ ocel bootstrap status",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = cmd.Help()
			return &exitsig.ExitError{Code: 1}
		},
	}

	cmd.AddCommand(
		newProvisionCommand(sess, environmentv1.Tier_TIER_PRODUCTION, []string{"prod"}),
		newProvisionCommand(sess, environmentv1.Tier_TIER_PREVIEW, nil),
		newDestroyCommand(sess),
		newPolicyCommand(sess),
		newStatusCommand(sess),
	)

	return cmd
}

func newProvisionCommand(sess session.Session, tier environmentv1.Tier, aliases []string) *cobra.Command {
	var opts Options

	name := Name(tier)
	cmd := &cobra.Command{
		Use:     name,
		Aliases: aliases,
		Short:   fmt.Sprintf("Set up or update the %s environment", name),
		Long: fmt.Sprintf("Set up or update the %s environment.\n\n", name) +
			"Interactive runs pick features from a list; --features sets the exact list to keep, " +
			"and anything left out is removed.",
		Example: fmt.Sprintf("  $ ocel bootstrap %s\n", name) +
			fmt.Sprintf("  $ ocel bootstrap %s --features core,queues\n", name) +
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

			ctx, stop := sess.Interrupt(cmd.Context(), cmd.ErrOrStderr())
			defer stop()

			return Run(ctx, sess, cwd, tier, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}

	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Skip confirmation")
	cmd.Flags().StringVar(&opts.Features, "features", "", "Comma-separated `set` of features to keep (also: all, none)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Remove a feature other projects still use")
	cmd.Flags().BoolVar(&opts.AutoHeal, "auto-heal", false, "Let later deploys refresh stale features on their own; --auto-heal=false turns it off")

	return cmd
}

func newDestroyCommand(sess session.Session) *cobra.Command {
	var opts Options

	cmd := &cobra.Command{
		Use:   "destroy <production|preview>",
		Short: "Tear down an environment with nothing deployed in it",
		Long: "Tear down an environment with nothing deployed in it.\n\n" +
			"Refuses while anything is still deployed there, and lists what has to go first. " +
			"Irreversible: requires typing the environment name to confirm; --yes skips that.",
		Example: "  $ ocel bootstrap destroy preview",
		Args:    cobra.MaximumNArgs(1),
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

			ctx, stop := sess.Interrupt(cmd.Context(), cmd.ErrOrStderr())
			defer stop()

			return RunDestroy(ctx, sess, cwd, tier, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}

	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Skip confirmation")

	return cmd
}

func newPolicyCommand(sess session.Session) *cobra.Command {
	return &cobra.Command{
		Use:   "policy <bootstrap|deploy>",
		Short: "Print the permissions bootstrap or deploy credentials need",
		Long: "Print the permissions bootstrap or deploy credentials need.\n\n" +
			"`bootstrap` is what bootstrapping runs under, `deploy` the smaller set deploys and " +
			"previews run under.",
		Example: "  $ ocel bootstrap policy deploy",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tier, err := credentialTierArg(args)
			if err != nil {
				_ = cmd.Help()
				return err
			}

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			ctx, stop := sess.Interrupt(cmd.Context(), cmd.ErrOrStderr())
			defer stop()

			return RunPolicy(ctx, sess, cwd, tier, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
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

func credentialTierArg(args []string) (contractv1.CredentialTier, error) {
	if len(args) == 0 {
		return contractv1.CredentialTier_CREDENTIAL_TIER_UNSPECIFIED,
			errors.New("name the credentials to print, bootstrap or deploy")
	}
	return credentialTier(args[0])
}

func Run(ctx context.Context, sess session.Session, cwd string, tier environmentv1.Tier, opts Options, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := resolveProject(ctx, sess, cwd)
	if err != nil {
		return err
	}

	prompter := prompt.New(stdout, stdin)
	return providerui.Run(ctx, sess, cfg, "ocel bootstrap "+Name(tier), stdout, func(ctx context.Context, runner *provider.Runner, ui *deployui.Session) error {
		client, err := runner.Client()
		if err != nil {
			return err
		}
		described, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{
			Tier:           tier,
			WithDependents: true,
		})
		if err != nil {
			if connect.CodeOf(err) == connect.CodeUnimplemented {
				return fmt.Errorf("%s cannot say which features a bootstrap has; it predates them. Upgrade the provider pinned in this project and try again", runner.Package())
			}
			return err
		}
		catalogue := described.GetFeatures()

		interactive := !opts.Yes && sess.StdinIsTerminal(stdin)
		requested, selected, err := chooseFeatures(ctx, prompter, opts, catalogue, interactive)
		if err != nil {
			return err
		}
		if !selected {
			fmt.Fprintln(stdout, "Aborted.")
			return nil
		}

		force := opts.Force
		dropped := droppedFeatures(enabledFeatures(catalogue), requested)
		if len(dropped) > 0 && !force {
			if !interactive {
				return fmt.Errorf("this would remove %s from the %s bootstrap; projects deployed against it break when it goes, so re-run with --force to remove it anyway",
					strings.Join(dropped, ", "), Name(tier))
			}
			confirmed, err := confirmDrop(ctx, prompter, tier, dropped, dependentProjects(catalogue, dropped), stdout)
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
			force = true
		}

		if status := described.GetBootstrap(); status.GetDowngrade() {
			fmt.Fprintln(stdout, downgradeWarning(tier, status))
			if interactive {
				proceed, err := prompter.Confirm(ctx, "Write the older content anyway?")
				if err != nil {
					return err
				}
				if !proceed {
					fmt.Fprintln(stdout, "Aborted.")
					return nil
				}
			}
		}

		if interactive {
			proceed, err := prompter.Confirm(ctx, fmt.Sprintf("Bootstrap %s infrastructure with %s?", Name(tier), runner.Package()))
			if err != nil {
				return err
			}
			if !proceed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		req := &contractv1.BootstrapRequest{
			Tier:               tier,
			Features:           requested,
			Force:              force,
			AcceptReplacements: opts.Yes,
		}
		if opts.AutoHealDeclared {
			req.AutoHeal = &opts.AutoHeal
		}
		if err := provider.Stream(ctx, runner, "Bootstrap", req, contractv1connect.ProviderServiceClient.Bootstrap, ui.Event); err != nil {
			return err
		}
		ui.Finish("Bootstrapped")
		return nil
	})
}

func resolveProject(ctx context.Context, sess session.Session, cwd string) (*projectconfig.Config, error) {
	cfg, err := projectconfig.Resolve(ctx, cwd, sess.ConfigPath())
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func RunPolicy(ctx context.Context, sess session.Session, cwd string, tier contractv1.CredentialTier, stdout, stderr io.Writer) error {
	cfg, err := resolveProject(ctx, sess, cwd)
	if err != nil {
		return err
	}

	return provider.Drive(ctx, cfg, stderr, stderr, func(runner *provider.Runner) error {
		client, err := runner.Client()
		if err != nil {
			return err
		}
		policy, err := client.GetCredentialPolicy(ctx, &contractv1.CredentialPolicyRequest{Tier: tier})
		if err != nil {
			if connect.CodeOf(err) == connect.CodeUnimplemented {
				return fmt.Errorf("%s cannot say what permissions these credentials need; it predates them. Upgrade the provider pinned in this project and try again", runner.Package())
			}
			return err
		}
		fmt.Fprintln(stdout, policy.GetDocument())
		return nil
	})
}

func credentialTier(requested string) (contractv1.CredentialTier, error) {
	switch requested {
	case "bootstrap":
		return contractv1.CredentialTier_CREDENTIAL_TIER_BOOTSTRAP, nil
	case "deploy":
		return contractv1.CredentialTier_CREDENTIAL_TIER_DEPLOY, nil
	default:
		return contractv1.CredentialTier_CREDENTIAL_TIER_UNSPECIFIED,
			fmt.Errorf("the credentials to print are bootstrap or deploy, not %q", requested)
	}
}

func confirmDrop(ctx context.Context, prompter prompt.Prompter, tier environmentv1.Tier, dropped, dependents []string, stdout io.Writer) (bool, error) {
	fmt.Fprintf(stdout, "Removing %s from the %s bootstrap tears down what it stood up.\n", strings.Join(dropped, ", "), Name(tier))
	if len(dependents) > 0 {
		fmt.Fprintf(stdout, "These projects were deployed against it and break when it goes: %s\n", strings.Join(dependents, ", "))
	} else {
		fmt.Fprintln(stdout, "No project deployed here has recorded needing it, but anything relying on it breaks.")
	}
	return prompter.Confirm(ctx, "Remove it anyway?")
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
