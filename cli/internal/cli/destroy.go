package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/node"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

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

		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		if err := checkDestroyFlags(destroyPreview, destroyYes); err != nil {
			return err
		}
		if destroyPreview {
			return runDestroyPreviewProject(ctx, defaultDeps(), cwd, destroyYes, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		}
		return runDestroy(ctx, defaultDeps(), cwd, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

var (
	destroyPreview bool
	destroyYes     bool
)

func init() {
	destroyCmd.Flags().BoolVar(&destroyPreview, "preview", false, "Destroy this project's entire preview footprint instead of production (leaves account-level preview bootstrap intact)")
	destroyCmd.Flags().BoolVarP(&destroyYes, "yes", "y", false, "With --preview only: destroy the whole preview footprint — every preview, ALL its data, assets and variables — with no confirmation and no terminal, for CI. Skips both the typed-name confirmation and the interactive-terminal requirement")
	rootCmd.AddCommand(destroyCmd)
}

func checkDestroyFlags(preview, yes bool) error {
	if yes && !preview {
		return errors.New("`ocel destroy --yes` is only accepted with --preview; destroying production always requires typing the project name at an interactive terminal")
	}
	return nil
}

func runDestroy(ctx context.Context, d deps, cwd string, stdout, stderr io.Writer, stdin io.Reader) error {
	if !isReaderTTY(stdin) {
		return errors.New("`ocel destroy` needs an interactive terminal to confirm the project name; it cannot be run non-interactively")
	}

	cfg, err := projectconfig.Resolve(cwd)
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

	ctx, run, err := startRun(ctx, cfg, "ocel destroy")
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sessionFormat(), verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, d, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		if err := preflightClass(ctx, d, runner, provider, deploymentsv1.Environment_CLASS_PRODUCTION, "ocel bootstrap", stdout); err != nil {
			return err
		}

		client, err := runner.Deployments()
		if err != nil {
			return err
		}

		spinner := deployui.StartSpinner(stdout, "Enumerating what would be destroyed")
		plan, err := client.PlanDestroyProject(ctx, &deploymentsv1.PlanDestroyProjectRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Slug:            cfg.Slug,
		})
		spinner.Stop()
		if err != nil {
			return err
		}
		if destroyPlanEmpty(plan) {
			ui.Finish("Nothing to destroy")
			return nil
		}

		printDestroyPlan(stdout, cfg.Slug, plan)
		confirmed, err := confirmDestroyProject(ctx, cfg.Slug, stdout, stdin)
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
			Slug:            cfg.Slug,
		}
		if err := runner.DestroyProject(ctx, req, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Destroyed project %s", cfg.Slug))
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

func runDestroyPreviewProject(ctx context.Context, d deps, cwd string, yes bool, stdout, stderr io.Writer, stdin io.Reader) error {
	if !yes && !isReaderTTY(stdin) {
		return errors.New("`ocel destroy --preview` needs an interactive terminal to confirm the project name; re-run with --yes to tear the preview footprint down non-interactively")
	}

	cfg, err := projectconfig.Resolve(cwd)
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

	ctx, run, err := startRun(ctx, cfg, "ocel destroy --preview")
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sessionFormat(), verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, d, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		if err := preflightClass(ctx, d, runner, provider, deploymentsv1.Environment_CLASS_PREVIEW, "ocel bootstrap --preview", stdout); err != nil {
			return err
		}

		fmt.Fprintf(stdout, "This will permanently destroy the ENTIRE preview footprint of project %q:\n", cfg.Slug)
		fmt.Fprintln(stdout, "  • every preview (persistent and ephemeral): app-deploy stacks, per-name infra stacks INCLUDING ALL DATA")
		fmt.Fprintln(stdout, "  • the project's preview deployments-store instance and preview edge worker(s)")
		fmt.Fprintln(stdout, "  • all stored preview assets belonging to this project")
		fmt.Fprintln(stdout, "  • every preview variable value this project holds, including each preview's own overrides")
		fmt.Fprintln(stdout, "The account-level preview bootstrap is left intact. This cannot be undone.")

		if !yes {
			confirmed, err := confirmDestroyProject(ctx, cfg.Slug, stdout, stdin)
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		req := &deploymentsv1.DestroyProjectRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Slug:            cfg.Slug,
			Environment:     &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW},
		}
		if err := runner.DestroyProject(ctx, req, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Destroyed preview footprint of project %s", cfg.Slug))
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

func destroyPlanEmpty(plan *deploymentsv1.PlanDestroyProjectResponse) bool {
	return !plan.GetRootStack() && plan.GetInfraStack() == "" && len(plan.GetAppStacks()) == 0
}

func printDestroyPlan(out io.Writer, slug string, plan *deploymentsv1.PlanDestroyProjectResponse) {
	fmt.Fprintf(out, "This will permanently destroy everything below in production project %q:\n", slug)
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
	fmt.Fprintln(out, "  • every production variable value this project holds, and their history")
	fmt.Fprintln(out, "This cannot be undone.")
}

func confirmDestroyProject(ctx context.Context, slug string, stdout io.Writer, stdin io.Reader) (bool, error) {
	fmt.Fprintf(stdout, "Type the project name (%s) to confirm: ", slug)

	line, err := readLine(ctx, stdin)
	if err != nil {
		if err == io.EOF {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(line) == slug, nil
}
