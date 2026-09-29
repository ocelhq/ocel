package deploy

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type deployOutcome struct {
	apps        []*progressv1.AppResult
	promotionID string
}

func streamDeploy(ctx context.Context, provider *providerprocess.Provider, req *contractv1.DeployRequest) (deployOutcome, error) {
	res, err := providerprocess.Stream(ctx, provider, "Deploy", req, contractv1connect.ProviderServiceClient.Deploy)
	return deployOutcome{apps: res.GetApps(), promotionID: res.GetPromotionId()}, err
}
