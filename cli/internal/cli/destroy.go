package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// destroyCmd permanently tears down a project's entire production deployment:
// the root stack, the infra stack (databases and buckets included), and every
// app-deploy stack. It is deliberately hard to trigger by accident — it always
// requires typing the project name at an interactive prompt and refuses to run
// without a terminal.
var destroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "Permanently destroy this project's entire production deployment",
	Long: "Permanently destroy this project's entire production deployment: the root stack " +
		"(edge workers, custom-domain binding, deployments store), the infra stack (databases " +
		"and buckets, including all their data), and every app-deploy stack.\n\n" +
		"This is irreversible and always requires typing the project name to confirm. It refuses " +
		"to run without an interactive terminal.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		if destroyPreview {
			return runDestroyPreviewProject(ctx, cwd, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		}
		return runDestroy(ctx, cwd, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

// destroyPreview selects the whole-project preview footprint instead of
// production for `ocel destroy`.
var destroyPreview bool

func init() {
	destroyCmd.Flags().BoolVar(&destroyPreview, "preview", false, "Destroy this project's entire preview footprint instead of production (leaves account-level preview bootstrap intact)")
	rootCmd.AddCommand(destroyCmd)
}

// runDestroy drives a production project teardown: it refuses without a
// terminal (the confirmation cannot be skipped), enumerates and shows the blast
// radius, requires the operator to type the project name, then drives the
// provider's DestroyProject RPC.
func runDestroy(ctx context.Context, cwd string, stdout, stderr io.Writer, stdin io.Reader) error {
	if _, err := loadCredentials(); err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &ExitError{Code: 1}
	}

	// A slip must never nuke production, so destroy has no --yes and no
	// non-interactive path: without a terminal to type the project name into,
	// refuse before touching anything.
	if !isReaderTTY(stdin) {
		return errors.New("`ocel destroy` needs an interactive terminal to confirm the project name; it cannot be run non-interactively")
	}

	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}
	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	ui := deployui.New(stdout, cfg.Dir, "ocel destroy", verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		if err := preflightClass(ctx, runner, provider, deploymentsv1.Environment_CLASS_PRODUCTION, "ocel bootstrap", stdout); err != nil {
			return err
		}

		spinner := deployui.StartSpinner(stdout, "Enumerating what would be destroyed")
		plan, err := runner.PlanDestroyProject(ctx, &deploymentsv1.PlanDestroyProjectRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			ProjectId:       cfg.ProjectID,
		})
		spinner.Stop()
		if err != nil {
			return err
		}
		if destroyPlanEmpty(plan) {
			ui.Finish("Nothing to destroy")
			return nil
		}

		printDestroyPlan(stdout, cfg.ProjectID, plan)
		confirmed, err := confirmDestroyProject(cfg.ProjectID, stdout, stdin)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(stdout, "Aborted.")
			return nil
		}

		req := &deploymentsv1.DestroyProjectRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			ProjectId:       cfg.ProjectID,
		}
		if err := runner.DestroyProject(ctx, req, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Destroyed project %s", cfg.ProjectID))
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

// runDestroyPreviewProject tears down a project's entire preview footprint: every
// preview pointer's app-deploy and per-name infra stacks, the preview store
// instance, the preview root worker(s), the R2 assets and the preview root-stack
// state — leaving the account-level preview bootstrap intact. Like production
// destroy it refuses without a terminal and requires typing the project name.
func runDestroyPreviewProject(ctx context.Context, cwd string, stdout, stderr io.Writer, stdin io.Reader) error {
	if _, err := loadCredentials(); err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &ExitError{Code: 1}
	}
	if !isReaderTTY(stdin) {
		return errors.New("`ocel destroy --preview` needs an interactive terminal to confirm the project name; it cannot be run non-interactively")
	}

	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}
	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	ui := deployui.New(stdout, cfg.Dir, "ocel destroy --preview", verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		if err := preflightClass(ctx, runner, provider, deploymentsv1.Environment_CLASS_PREVIEW, "ocel bootstrap --preview", stdout); err != nil {
			return err
		}

		fmt.Fprintf(stdout, "This will permanently destroy the ENTIRE preview footprint of project %q:\n", cfg.ProjectID)
		fmt.Fprintln(stdout, "  • every preview (persistent and ephemeral): app-deploy stacks, per-name infra stacks INCLUDING ALL DATA")
		fmt.Fprintln(stdout, "  • the project's preview deployments-store instance and preview edge worker(s)")
		fmt.Fprintln(stdout, "  • all stored preview assets belonging to this project")
		fmt.Fprintln(stdout, "The account-level preview bootstrap is left intact. This cannot be undone.")

		confirmed, err := confirmDestroyProject(cfg.ProjectID, stdout, stdin)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(stdout, "Aborted.")
			return nil
		}

		req := &deploymentsv1.DestroyProjectRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			ProjectId:       cfg.ProjectID,
			Environment:     &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW},
		}
		if err := runner.DestroyProject(ctx, req, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Destroyed preview footprint of project %s", cfg.ProjectID))
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

// destroyPlanEmpty reports whether a project has nothing left to destroy — no
// root stack, no infra stack, and no app stacks — so destroy can exit cleanly
// without prompting.
func destroyPlanEmpty(plan *deploymentsv1.PlanDestroyProjectResponse) bool {
	return !plan.GetRootStack() && plan.GetInfraStack() == "" && len(plan.GetAppStacks()) == 0
}

// printDestroyPlan renders the blast radius the operator is about to confirm, so
// they type the project name against a real inventory rather than blind.
func printDestroyPlan(out io.Writer, projectID string, plan *deploymentsv1.PlanDestroyProjectResponse) {
	fmt.Fprintf(out, "This will permanently destroy everything below in production project %q:\n", projectID)
	if plan.GetRootStack() {
		fmt.Fprintln(out, "  • root stack — edge workers, custom-domain binding, deployments store")
	}
	if s := plan.GetInfraStack(); s != "" {
		fmt.Fprintf(out, "  • infra stack %s — databases and buckets, INCLUDING ALL DATA\n", s)
	}
	for _, s := range plan.GetAppStacks() {
		fmt.Fprintf(out, "  • app stack %s\n", s)
	}
	fmt.Fprintln(out, "  • all stored assets belonging to this project")
	fmt.Fprintln(out, "This cannot be undone.")
}

// confirmDestroyProject requires the operator to type the exact project name.
// The match is case-sensitive and exact so a keyboard slip — or a reflexive
// "y" — never proceeds. An empty or closed stdin reads as "no".
func confirmDestroyProject(projectID string, stdout io.Writer, stdin io.Reader) (bool, error) {
	fmt.Fprintf(stdout, "Type the project name (%s) to confirm: ", projectID)

	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, fmt.Errorf("failed to read input: %w", err)
		}
		return false, nil
	}
	return strings.TrimSpace(scanner.Text()) == projectID, nil
}
