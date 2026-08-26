package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"

	"charm.land/huh/v2"

	"github.com/ocelhq/ocel/cli/internal/changeplan"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/providerui"
	"github.com/ocelhq/ocel/cli/internal/cli/style"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func RunDestroy(ctx context.Context, deps cmddeps.Deps, cwd string, tier environmentv1.Tier, opts Options, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := resolveProject(ctx, deps, cwd)
	if err != nil {
		return err
	}
	return runDestroy(ctx, deps, cfg, tier, opts, stdout, stderr, stdin)
}

func runDestroy(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, tier environmentv1.Tier, opts Options, stdout, stderr io.Writer, stdin io.Reader) error {
	name := Name(tier)
	requested := changeplan.BypassRequest()
	bypass := requested == name
	tty := deps.StdinIsTerminal(stdin)
	switch {
	case opts.Dry:
	case bypass:
		fmt.Fprintf(stderr, "%s=%s: removing the %s bootstrap without confirmation\n", changeplan.BypassEnv, name, name)
	case requested == "" || opts.Yes:
	case !tty:
		return fmt.Errorf("%s is set to %q, but this is the %s bootstrap; it must name the bootstrap being removed", changeplan.BypassEnv, requested, name)
	default:
		fmt.Fprintf(stderr, "%s is set to %q, not this bootstrap (%s); confirming interactively instead\n", changeplan.BypassEnv, requested, name)
	}
	skipConfirmation := opts.Yes || bypass
	if !opts.Dry && !skipConfirmation && !tty {
		return fmt.Errorf("`%s` needs an interactive terminal to confirm the environment name; re-run with --yes, or set %s to %q, to remove it unattended",
			destroyCommand(tier), changeplan.BypassEnv, name)
	}

	return providerui.Run(ctx, deps, cfg, destroyCommand(tier), stdout, func(ctx context.Context, runner *provider.Runner, ui *deployui.Session) error {
		client, err := runner.Client()
		if err != nil {
			return err
		}

		spinner := deployui.StartSpinner(stdout, "Enumerating what would be removed")
		plan, err := client.PlanRemoveBootstrap(ctx, &contractv1.BootstrapScope{
			Tier: tier,
			Edge: edgewire.Selection(cfg),
		})
		spinner.Stop()
		if err != nil {
			return err
		}
		if len(plan.GetGroups()) == 0 {
			ui.Finish(fmt.Sprintf("Nothing to destroy: the %s environment is not bootstrapped", name))
			return nil
		}
		changeplan.NewPrinter(stdout).Print(fmt.Sprintf("This will permanently remove the %s bootstrap", name), plan,
			"Every app already deployed from it keeps running and nothing can describe, update or remove it again. This cannot be undone.")
		if opts.Dry {
			fmt.Fprintln(stdout, "Run without --dry to destroy.")
			return nil
		}
		if !skipConfirmation {
			subject := plan.GetSubject()
			var typed string
			err := huh.NewForm(huh.NewGroup(
				huh.NewInput().
					Title("Type the environment name (" + subject + ") to confirm").
					Value(&typed),
			)).WithTheme(style.Theme).RunWithContext(ctx)
			if err != nil && !errors.Is(err, huh.ErrUserAborted) {
				return err
			}
			if errors.Is(err, huh.ErrUserAborted) || subject == "" || typed != subject {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		req := &contractv1.BootstrapScope{
			Tier: tier,
			Edge: edgewire.Selection(cfg),
		}
		if err := provider.Stream(ctx, runner, "RemoveBootstrap", req, contractv1connect.ProviderServiceClient.RemoveBootstrap, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Removed the %s bootstrap", name))
		return nil
	})
}

func destroyCommand(tier environmentv1.Tier) string {
	return "ocel bootstrap destroy " + Name(tier)
}
