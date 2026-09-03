package bootstrap

import (
	"context"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/preflight"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
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
	bypass, err := runui.Bypass{
		Noun:          "bootstrap",
		Subject:       name,
		Action:        fmt.Sprintf("removing the %s bootstrap", name),
		Verb:          "removed",
		Yes:           opts.Yes,
		Dry:           opts.Dry,
		GrantsWhenDry: true,
		TTY:           deps.StdinIsTerminal(stdin),
	}.Granted(stderr)
	if err != nil {
		return err
	}

	spec := deps.Spec(runui.PlanFirst, destroyCommand(tier), cfg, opts.Yes || bypass, stdout, stdin)
	spec.Dry = opts.Dry
	spec.Unattended = fmt.Sprintf("pass --yes, or set %s to %q", runui.BypassEnv, name)

	return runui.Run(ctx, spec, func(ctx context.Context, runner *provider.Runner, ui *runui.Session) error {
		if err := preflight.Announce(ctx, ui, runner, cfg, tier); err != nil {
			return err
		}
		client, err := runner.Client()
		if err != nil {
			return err
		}

		spinner := ui.Spin("Enumerating what would be removed")
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
		consented := ui.Plan(fmt.Sprintf("This will permanently remove the %s bootstrap", name), plan,
			"Every app already deployed from it keeps running and nothing can describe, update or remove it again. This cannot be undone.")
		if opts.Dry {
			ui.Diagnostic("Run without --dry to destroy.")
			return nil
		}
		granted, err := ui.ConsentByName(ctx, "environment name", plan.GetSubject())
		if err != nil || !granted {
			return err
		}

		req := &contractv1.BootstrapScope{
			Tier:      tier,
			Edge:      edgewire.Selection(cfg),
			Consented: consented,
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
