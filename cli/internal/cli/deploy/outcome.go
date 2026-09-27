package deploy

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/inlinebinding"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type deployOutcome struct {
	bindings    []*bindingsv1.Binding
	apps        []*progressv1.AppResult
	urlNotes    []string
	promotionID string
	flip        *progressv1.FlipBound
}

func streamDeploy(ctx context.Context, prov *providerclient.Provider, slug string, req *contractv1.DeployRequest, inline []inlinebinding.Record) (deployOutcome, error) {
	var out deployOutcome
	records, err := prov.Vars()
	if err != nil {
		return out, err
	}
	env := req.GetEnvironment()
	at := inlinebinding.Coordinate{Slug: slug, Tier: env.GetTier(), Environment: env.GetIdentity()}
	err = inlinebinding.Deploy(ctx, records, at, inline, func() error {
		res, err := providerclient.Stream(ctx, prov, "Deploy", req, contractv1connect.ProviderServiceClient.Deploy)
		out = deployOutcome{
			bindings:    res.GetBindings(),
			apps:        res.GetApps(),
			urlNotes:    res.GetUrlNotes(),
			promotionID: res.GetPromotionId(),
			flip:        res.GetFlipBound(),
		}
		return err
	})
	return out, err
}
