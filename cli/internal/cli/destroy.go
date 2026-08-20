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
	Long: "Permanently destroy this project's entire production deployment: the edge stack " +
		"(edge workers, custom-domain binding, deployments store), the infra stack (databases " +
		"and buckets, including all their data), and every app-deploy stack.\n\n" +
		"This is irreversible and requires typing the project name to confirm; it refuses to run " +
		"without an interactive terminal.\n\n" +
		"An automated caller that must tear its own project down unattended can set " +
		destroyBypassEnv + " to the project name — and only that name — to skip both gates. " +
		"Any other value is not a bypass.",
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
		return fmt.Errorf("`ocel destroy --yes` is only accepted with --preview; destroying production requires typing the project name at an interactive terminal, or %s set to the project name", destroyBypassEnv)
	}
	return nil
}

const destroyBypassEnv = "OCEL_DESTROY_BYPASS_CONFIRMATION"

func destroyBypassRequest() string {
	return strings.TrimSpace(os.Getenv(destroyBypassEnv))
}

func runDestroy(ctx context.Context, d deps, cwd string, stdout, stderr io.Writer, stdin io.Reader) error {
	requested := destroyBypassRequest()
	tty := isReaderTTY(stdin)
	if requested == "" && !tty {
		return fmt.Errorf("`ocel destroy` needs an interactive terminal to confirm the project name; to destroy unattended, set %s to the project name", destroyBypassEnv)
	}

	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	bypass := requested == cfg.Slug
	switch {
	case bypass:
		fmt.Fprintf(stderr, "%s=%s: destroying production without confirmation\n", destroyBypassEnv, cfg.Slug)
	case requested != "" && !tty:
		return fmt.Errorf("%s is set to %q, but this project is %q; it must name the project being destroyed", destroyBypassEnv, requested, cfg.Slug)
	case requested != "":
		fmt.Fprintf(stderr, "%s is set to %q, not this project (%s); confirming interactively instead\n", destroyBypassEnv, requested, cfg.Slug)
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
		if err := preflightClass(ctx, d, runner, provider, cfg, deploymentsv1.Environment_CLASS_PRODUCTION, "ocel bootstrap", stdout); err != nil {
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
			Edge:            edgeSelection(cfg),
		})
		spinner.Stop()
		if err != nil {
			return err
		}
		if plan.GetNothingToDestroy() {
			ui.Finish("Nothing to destroy")
			return nil
		}

		printDestroyPlan(stdout, cfg.Slug, false, plan)
		if !bypass {
			confirmed, err := confirmPhrase(ctx, "project name", cfg.Slug, stdout, stdin)
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
			Edge:            edgeSelection(cfg),
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

	ctx, run, err := startRun(ctx, cfg, "ocel destroy --preview")
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sessionFormat(), verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, d, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		if err := preflightClass(ctx, d, runner, provider, cfg, deploymentsv1.Environment_CLASS_PREVIEW, "ocel bootstrap --preview", stdout); err != nil {
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
			Environment:     &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW},
			Edge:            edgeSelection(cfg),
		})
		spinner.Stop()
		if err != nil {
			return err
		}
		if plan.GetNothingToDestroy() {
			ui.Finish("Nothing to destroy")
			return nil
		}

		printDestroyPlan(stdout, cfg.Slug, true, plan)

		if !yes {
			confirmed, err := confirmPhrase(ctx, "project name", cfg.Slug, stdout, stdin)
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
			Edge:            edgeSelection(cfg),
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

func printDestroyPlan(out io.Writer, slug string, preview bool, plan *deploymentsv1.PlanDestroyProjectResponse) {
	header := fmt.Sprintf("This will permanently destroy production project %q", slug)
	if preview {
		header = fmt.Sprintf("This will permanently destroy the ENTIRE preview footprint of project %q", slug)
	}
	if kind := plan.GetEdgeStack().GetEdgeKind(); kind != "" {
		header += fmt.Sprintf(", fronted by the %s edge", kind)
	}
	fmt.Fprintf(out, "%s:\n", header)

	kept := printPlanItems(out, plan.GetEdgeStack().GetItems())
	for _, s := range plan.GetInfraStacks() {
		fmt.Fprintf(out, "  • infra stack %s — databases and buckets, INCLUDING ALL DATA\n", s)
	}
	for _, s := range plan.GetAppStacks() {
		fmt.Fprintf(out, "  • app stack %s\n", s)
	}
	if preview {
		fmt.Fprintln(out, "  • all stored preview assets belonging to this project")
		fmt.Fprintln(out, "  • every preview variable value this project holds, including each preview's own overrides")
		fmt.Fprintln(out, "The account-level preview bootstrap is left intact. This cannot be undone.")
	} else {
		fmt.Fprintln(out, "  • all stored assets belonging to this project")
		fmt.Fprintln(out, "  • every production variable value this project holds, and their history")
		fmt.Fprintln(out, "This cannot be undone.")
	}
	printKeptItems(out, kept)
}

func printPlanItems(out io.Writer, items []*deploymentsv1.TeardownItem) []*deploymentsv1.TeardownItem {
	var kept []*deploymentsv1.TeardownItem
	for _, item := range items {
		if item.GetAction() == deploymentsv1.TeardownItem_ACTION_KEEP {
			kept = append(kept, item)
			continue
		}
		fmt.Fprintf(out, "  • %s\n", teardownItemLine(item))
	}
	return kept
}

func printKeptItems(out io.Writer, kept []*deploymentsv1.TeardownItem) {
	if len(kept) == 0 {
		return
	}
	fmt.Fprintln(out, "Left in place:")
	for _, item := range kept {
		fmt.Fprintf(out, "  • %s\n", teardownItemLine(item))
	}
}

func teardownItemLine(item *deploymentsv1.TeardownItem) string {
	line := fmt.Sprintf("%s %s %s", teardownItemAction(item.GetAction()), item.GetKind(), item.GetName())
	if reason := item.GetReason(); reason != "" {
		line += " — " + reason
	}
	if item.GetSlow() {
		line += " (this one is slow)"
	}
	return line
}

func teardownItemAction(action deploymentsv1.TeardownItem_Action) string {
	switch action {
	case deploymentsv1.TeardownItem_ACTION_DELETE:
		return "delete"
	case deploymentsv1.TeardownItem_ACTION_DISABLE_THEN_DELETE:
		return "disable, then delete"
	case deploymentsv1.TeardownItem_ACTION_KEEP:
		return "keep"
	default:
		return fmt.Sprintf("act on (%s, an action this CLI does not know)", action)
	}
}
