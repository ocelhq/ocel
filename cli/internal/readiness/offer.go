package readiness

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func hasOffer(ctx context.Context, provider *providerprocess.Provider, tier environmentv1.Tier, edge *contractv1.EdgeSelection, feature string) (bool, error) {
	var described *contractv1.DescribeBootstrapResponse
	err := provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		described, err = client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{Tier: tier, Edge: edge})
		return err
	})
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(described.GetFeatures(), func(offered *contractv1.Feature) bool {
		return offered.GetName() == feature
	}), nil
}
