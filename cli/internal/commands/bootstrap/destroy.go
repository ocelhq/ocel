package bootstrap

import (
	"context"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
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
		Interactive:  invocation.CanAsk(stdin),
	}.Granted()
	if err != nil {
		return err
	}

	if _, err := invocation.RequireProvider(ctx, cfg, destroyCommand(tier)); err != nil {
		return err
	}
	policy := consent.NewPlanPolicy(destroyCommand(tier), opts.Yes || bypass, invocation.CanAsk(stdin), stdout, stdin)
	policy.DryRun = opts.Dry
	policy.UnattendedRemedy = fmt.Sprintf("pass --yes, or set %s to %q", consent.BypassEnv, name)
	if err := policy.Refuse(); err != nil {
		return err
	}

	ctx, running, err := invocation.Events.Begin(ctx, destroyCommand(tier), cfg.Dir)
	if err != nil {
		return err
	}
	defer running.End(&err)

	check := running.Phase(progressv1.Phase_PHASE_CHECK)
	if notice != "" {
		check.Warn(notice)
	}
	provider, _, err := invocation.OpenProvider(ctx, check, cfg, commands.OpenOptions{Intent: commands.ManageBootstrap, Tier: tier, Policy: policy, DryRun: policy.DryRun})
	check.End(err)
	if err != nil {
		return err
	}
	defer provider.Close()

	planning := running.Phase(progressv1.Phase_PHASE_PLAN)
	enumeratingCtx, span := planning.Child(ctx, name, progress.Enumerating.Title(fmt.Sprintf("what removing the %s bootstrap would delete", name)))
	var plan *planv1.ChangePlan
	err = provider.Call(enumeratingCtx, func(client contractv1connect.ProviderServiceClient) (err error) {
		plan, err = client.PlanRemoveBootstrap(enumeratingCtx, &contractv1.BootstrapScope{
			Tier: tier,
			Edge: cfg.EdgeSelection(),
		})
		return err
	})
	span.End(err)
	if err != nil {
		return err
	}
	if len(plan.GetGroups()) == 0 {
		running.Succeed(fmt.Sprintf("Nothing to destroy: the %s environment is not bootstrapped", name))
		return nil
	}
	consented := planning.Plan(fmt.Sprintf("This will permanently remove the %s bootstrap", name), plan,
		&planv1.Note{Text: "Every app already deployed from it keeps running and nothing can describe, update or remove it again. This cannot be undone."})
	if opts.Dry {
		planning.Say("Run without --dry to destroy.")
		running.Succeed(fmt.Sprintf("Planned the removal of the %s bootstrap", name))
		return nil
	}
	granted, err := policy.ConfirmPlanByName(ctx, planning, consented, "environment name", plan.GetSubject())
	if err != nil || !granted {
		planning.End(err)
	}
	if err != nil {
		return err
	}
	if !granted {
		running.Succeed(fmt.Sprintf("Nothing removed: the %s bootstrap stays", name))
		return nil
	}

	if provider.Facts().GetWebIdentity() != nil {
		described, err := describeBootstrap(ctx, planning, provider, cfg, tier)
		if err != nil {
			return err
		}
		if err := forgetConsoleTrust(ctx, invocation, described.GetBootstrap().GetTrust(), tier, policy, planning); err != nil {
			return err
		}
	}
	planning.End(nil)
	req := &contractv1.BootstrapScope{
		Tier:      tier,
		Edge:      cfg.EdgeSelection(),
		Consented: consented,
	}
	if _, err := providerprocess.Stream(ctx, provider, "RemoveBootstrap", req, contractv1connect.ProviderServiceClient.RemoveBootstrap); err != nil {
		return err
	}
	running.Succeed(fmt.Sprintf("Removed the %s bootstrap", name))
	return nil
}

func destroyCommand(tier environmentv1.Tier) string {
	return "ocel bootstrap destroy " + readiness.TierName(tier)
}
