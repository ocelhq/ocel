package deploy

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

const dryFlagUsage = "Print every change this would make to your account and stop, changing nothing"

func showDeployPlan(ctx context.Context, runner *provider.Runner, ui *runui.Session, req *contractv1.DeployRequest, headline string) error {
	var plan *planv1.ChangePlan
	err := provider.Stream(ctx, runner, "Deploy", req, contractv1connect.ProviderServiceClient.Deploy,
		func(ev *progressv1.OperationEvent) {
			if shown := ev.GetPlan(); shown != nil {
				plan = shown
				return
			}
			if ev.GetResult().GetSuccess() {
				return
			}
			ui.Event(ev)
		})
	if err != nil {
		return err
	}
	if len(plan.GetGroups()) == 0 {
		ui.Finish("Nothing to change")
		return nil
	}
	ui.Plan(headline, plan)
	ui.Diagnostic("Run without --dry to apply.")
	return nil
}
