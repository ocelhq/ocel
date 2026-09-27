package deploy

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

const dryFlagUsage = "Build, then print every change this would make to your account and stop without applying any of it"

func showDeployPlan(ctx context.Context, run *events.Run, prov *providerclient.Provider, req *contractv1.DeployRequest, headline, slug, place string) error {
	plan, err := providerclient.Plan(ctx, prov, "Deploy", req, contractv1connect.ProviderServiceClient.Deploy)
	if err != nil {
		return err
	}
	if len(plan.GetGroups()) == 0 {
		run.Finish("Nothing to change in " + place)
		return nil
	}
	scope := run.Phase(progressv1.Phase_PHASE_PLAN)
	scope.Plan(headline, plan)
	scope.Say("Run without --dry to apply.")
	scope.End(nil)
	run.Finish(fmt.Sprintf("Planned the deploy of %s to %s", slug, place))
	return nil
}
