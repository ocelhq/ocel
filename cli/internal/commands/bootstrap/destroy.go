package bootstrap

import (
	"context"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func RunDestroy(ctx context.Context, invocation commands.Invocation, cwd string, tier environmentv1.Tier, opts Options, stdout io.Writer, stdin io.Reader) error {
	cfg, err := resolveProject(ctx, invocation, cwd)
	if err != nil {
		return err
	}
	return runDestroy(ctx, invocation, cfg, tier, opts, stdout, stdin)
}

func runDestroy(ctx context.Context, invocation commands.Invocation, cfg *project.Project, tier environmentv1.Tier, opts Options, stdout io.Writer, stdin io.Reader) (err error) {
	name := readiness.TierName(tier)
	bypass, notice, err := consent.Bypass{
		Noun:         "bootstrap",
		Subject:      name,
		Action:       fmt.Sprintf("removing the %s bootstrap", name),
		Verb:         "removed",
		Yes:          opts.Yes,
		DryRun:       opts.Dry,
		GrantsDryRun: true,
		TTY:          invocation.StdinIsTerminal(stdin),
	}.Granted()
	if err != nil {
		return err
	}

	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}
	policy := consent.NewPolicy(destroyCommand(tier), opts.Yes || bypass, invocation.StdinIsTerminal(stdin), stdout, stdin)
	policy.ConfirmsPlan = true
	policy.DryRun = opts.Dry
	policy.UnattendedRemedy = fmt.Sprintf("pass --yes, or set %s to %q", consent.BypassEnv, name)
	if err := policy.Refuse(); err != nil {
		return err
	}

	ctx, run, err := invocation.Events.Begin(ctx, destroyCommand(tier), cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	if notice != "" {
		check.Warn(notice)
	}
	provider, _, err := invocation.OpenProvider(ctx, check, cfg, commands.OpenOptions{
		Pinning: executables.ChoosePinning(opts.Dry),
		Tier:    tier,
		Require: readiness.Credentials,
		Slug:    cfg.Slug,
	})
	check.End(err)
	if err != nil {
		return err
	}
	defer provider.Close()

	planning := run.Phase(progressv1.Phase_PHASE_PLAN)
	unit := planning.Unit(name, progress.Enumerating.Title(fmt.Sprintf("what removing the %s bootstrap would delete", name)))
	var plan *planv1.ChangePlan
	err = provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
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
		run.Succeed(fmt.Sprintf("Nothing to destroy: the %s environment is not bootstrapped", name))
		return nil
	}
	consented := planning.Plan(fmt.Sprintf("This will permanently remove the %s bootstrap", name), plan,
		&planv1.Note{Text: "Every app already deployed from it keeps running and nothing can describe, update or remove it again. This cannot be undone."})
	if opts.Dry {
		planning.Say("Run without --dry to destroy.")
		run.Succeed(fmt.Sprintf("Planned the removal of the %s bootstrap", name))
		return nil
	}
	granted, err := policy.ConfirmPlanByName(ctx, planning, consented, "environment name", plan.GetSubject())
	planning.End(err)
	if err != nil {
		return err
	}
	if !granted {
		run.Succeed(fmt.Sprintf("Nothing removed: the %s bootstrap stays", name))
		return nil
	}

	req := &contractv1.BootstrapScope{
		Tier:      tier,
		Edge:      cfg.EdgeSelection(),
		Consented: consented,
	}
	if _, err := providerprocess.Stream(ctx, provider, "RemoveBootstrap", req, contractv1connect.ProviderServiceClient.RemoveBootstrap); err != nil {
		return err
	}
	run.Succeed(fmt.Sprintf("Removed the %s bootstrap", name))
	return nil
}

func destroyCommand(tier environmentv1.Tier) string {
	return "ocel bootstrap destroy " + readiness.TierName(tier)
}
