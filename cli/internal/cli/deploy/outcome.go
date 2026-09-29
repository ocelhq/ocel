package deploy

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/inlinebinding"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type deployOutcome struct {
	apps        []*progressv1.AppResult
	urlNotes    []string
	promotionID string
	propagation *progressv1.Propagation
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
			apps:        res.GetApps(),
			urlNotes:    res.GetUrlNotes(),
			promotionID: res.GetPromotionId(),
			propagation: res.GetPropagation(),
		}
		return err
	})
	return out, err
}
