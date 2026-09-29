package deploy

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/run"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

const dryFlagUsage = "Build, then print every change this would make to your account and stop without applying any of it"

func showDeployPlan(ctx context.Context, run *run.Run, prov *providerclient.Provider, req *contractv1.DeployRequest, headline, slug, place string) error {
	plan, err := providerclient.Plan(ctx, prov, "Deploy", req, contractv1connect.ProviderServiceClient.Deploy)
	if err != nil {
		return err
	}
	if len(plan.GetGroups()) == 0 {
		run.Succeed("Nothing to change in " + place)
		return nil
	}
	span := run.Phase(progressv1.Phase_PHASE_PLAN)
	span.Plan(headline, plan)
	span.Say("Run without --dry to apply.")
	span.End(nil)
	run.Succeed(fmt.Sprintf("Planned the deploy of %s to %s", slug, place))
	return nil
}
