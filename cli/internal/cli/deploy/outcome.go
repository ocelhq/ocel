package deploy

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/inlinebinding"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/runui"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type deployOutcome struct {
	bindings    []*bindingsv1.Binding
	functions   []*progressv1.FunctionOutput
	apps        []*progressv1.AppResult
	urlNotes    []string
	promotionID string
	flip        runui.Flip
}

func (o *deployOutcome) collect(ui *runui.Session) func(*progressv1.OperationEvent) {
	return func(ev *progressv1.OperationEvent) {
		ui.Event(ev)
		res := ev.GetResult()
		if res == nil {
			return
		}
		o.bindings = res.GetBindings()
		o.functions = res.GetFunctions()
		o.apps = res.GetApps()
		o.urlNotes = res.GetUrlNotes()
		o.promotionID = res.GetPromotionId()
		o.flip = runui.FlipFor(res.GetFlipBound())
	}
}

func streamDeploy(ctx context.Context, runner *providerclient.Runner, ui *runui.Session, slug string, req *contractv1.DeployRequest, inline []inlinebinding.Record) (deployOutcome, error) {
	var out deployOutcome
	records, err := runner.Vars()
	if err != nil {
		return out, err
	}
	env := req.GetEnvironment()
	at := inlinebinding.Coordinate{Slug: slug, Tier: env.GetTier(), Environment: env.GetIdentity()}
	err = inlinebinding.Deploy(ctx, records, at, inline, func() error {
		return providerclient.Stream(ctx, runner, "Deploy", req, contractv1connect.ProviderServiceClient.Deploy, out.collect(ui))
	})
	return out, err
}
