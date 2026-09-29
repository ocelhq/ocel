package bootstrap

import (
	"context"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/preflight"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func RunDestroy(ctx context.Context, deps cmddeps.Deps, cwd string, tier environmentv1.Tier, opts Options, stdout io.Writer, stdin io.Reader) error {
	cfg, err := resolveProject(ctx, deps, cwd)
	if err != nil {
		return err
	}
	return runDestroy(ctx, deps, cfg, tier, opts, stdout, stdin)
}

func runDestroy(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, tier environmentv1.Tier, opts Options, stdout io.Writer, stdin io.Reader) (err error) {
	name := Name(tier)
	bypass, notice, err := consent.Bypass{
		Noun:          "bootstrap",
		Subject:       name,
		Action:        fmt.Sprintf("removing the %s bootstrap", name),
		Verb:          "removed",
		Yes:           opts.Yes,
		Dry:           opts.Dry,
		GrantsWhenDry: true,
		TTY:           deps.StdinIsTerminal(stdin),
	}.Granted()
	if err != nil {
		return err
	}

	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}
	gate := deps.Gate(consent.PlanFirst, destroyCommand(tier), opts.Yes || bypass, stdout, stdin)
	gate.Dry = opts.Dry
	gate.Unattended = fmt.Sprintf("pass --yes, or set %s to %q", consent.BypassEnv, name)
	if err := gate.Refuse(); err != nil {
		return err
	}

	ctx, run, err := deps.Events.Begin(ctx, destroyCommand(tier), cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	if notice != "" {
		check.Warn(notice)
	}
	prov, err := providerclient.Start(ctx, cfg, check, deps.HostTrust, providerclient.ChoosePinning(opts.Dry))
	if err != nil {
		return err
	}
	defer prov.Close()

	err = preflight.Announce(ctx, check, prov, cfg, tier)
	check.End(err)
	if err != nil {
		return err
	}

	planning := run.Phase(progressv1.Phase_PHASE_PLAN)
	unit := planning.Unit(name, fmt.Sprintf("Enumerating what removing the %s bootstrap would delete", name))
	var plan *planv1.ChangePlan
	err = prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		plan, err = client.PlanRemoveBootstrap(ctx, &contractv1.BootstrapScope{
			Tier: tier,
			Edge: cfg.EdgeSelection(),
		})
		return err
	})
	unit.End(err)
	if err != nil {
		return err
	}
	if len(plan.GetGroups()) == 0 {
		run.Finish(fmt.Sprintf("Nothing to destroy: the %s environment is not bootstrapped", name))
		return nil
	}
	consented := planning.Plan(fmt.Sprintf("This will permanently remove the %s bootstrap", name), plan,
		"Every app already deployed from it keeps running and nothing can describe, update or remove it again. This cannot be undone.")
	if opts.Dry {
		planning.Say("Run without --dry to destroy.")
		run.Finish(fmt.Sprintf("Planned the removal of the %s bootstrap", name))
		return nil
	}
	granted, err := gate.ConsentByName(ctx, planning, consented, "environment name", plan.GetSubject())
	planning.End(err)
	if err != nil {
		return err
	}
	if !granted {
		run.Finish(fmt.Sprintf("Nothing removed: the %s bootstrap stays", name))
		return nil
	}

	req := &contractv1.BootstrapScope{
		Tier:      tier,
		Edge:      cfg.EdgeSelection(),
		Consented: consented,
	}
	if _, err := providerclient.Stream(ctx, prov, "RemoveBootstrap", req, contractv1connect.ProviderServiceClient.RemoveBootstrap); err != nil {
		return err
	}
	run.Finish(fmt.Sprintf("Removed the %s bootstrap", name))
	return nil
}

func destroyCommand(tier environmentv1.Tier) string {
	return "ocel bootstrap destroy " + Name(tier)
}
