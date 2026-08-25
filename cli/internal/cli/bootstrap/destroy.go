package bootstrap

import (
	"context"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/cli/session"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/obs"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/prompt"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/internal/providersession"
	"github.com/ocelhq/ocel/cli/internal/removalplan"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func RunDestroy(ctx context.Context, sess session.Session, cwd string, tier environmentv1.Tier, opts Options, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, provider, err := resolveProject(ctx, sess, cwd)
	if err != nil {
		return err
	}
	return runDestroy(ctx, sess, cfg, provider, tier, opts, stdout, stderr, stdin)
}

func runDestroy(ctx context.Context, sess session.Session, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor, tier environmentv1.Tier, opts Options, stdout, stderr io.Writer, stdin io.Reader) error {
	name := Name(tier)
	requested := removalplan.BypassRequest()
	bypass := requested == name
	tty := sess.StdinIsTerminal(stdin)
	switch {
	case bypass:
		fmt.Fprintf(stderr, "%s=%s: removing the %s bootstrap without confirmation\n", removalplan.BypassEnv, name, name)
	case requested == "" || opts.Yes:
	case !tty:
		return fmt.Errorf("%s is set to %q, but this is the %s bootstrap; it must name the bootstrap being removed", removalplan.BypassEnv, requested, name)
	default:
		fmt.Fprintf(stderr, "%s is set to %q, not this bootstrap (%s); confirming interactively instead\n", removalplan.BypassEnv, requested, name)
	}
	skipConfirmation := opts.Yes || bypass
	if !skipConfirmation && !tty {
		return fmt.Errorf("`%s` needs an interactive terminal to confirm the environment name; re-run with --yes, or set %s to %q, to remove it unattended",
			destroyCommand(tier), removalplan.BypassEnv, name)
	}

	ctx, run, err := obs.Start(ctx, cfg.Dir, destroyCommand(tier))
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sess.Format(), sess.Verbose())
	defer ui.Close()

	asked := prompt.New(stdout, stdin)
	provW := ui.BuildWriter()
	err = providersession.Drive(ctx, sess.LocateProviderBinary, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		client, err := runner.Deployments()
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
		removalplan.Print(stdout, fmt.Sprintf("This will permanently remove the %s bootstrap", name), plan,
			"Every app already deployed from it keeps running and nothing can describe, update or remove it again. This cannot be undone.")
		if !skipConfirmation {
			confirmed, err := asked.Phrase(ctx, "environment name", plan.GetSubject())
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		req := &contractv1.BootstrapScope{
			Tier: tier,
			Edge: edgewire.Selection(cfg),
		}
		if err := providerrunner.Stream(ctx, runner, "RemoveBootstrap", req, contractv1connect.ProviderServiceClient.RemoveBootstrap, ui.Event); err != nil {
			return err
		}
		ui.Finish(fmt.Sprintf("Removed the %s bootstrap", name))
		return nil
	})
	if err != nil {
		return providersession.Fail(ctx, ui, err)
	}
	return nil
}

func destroyCommand(tier environmentv1.Tier) string {
	return "ocel bootstrap destroy " + Name(tier)
}
