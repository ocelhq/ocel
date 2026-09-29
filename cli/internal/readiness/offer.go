package readiness

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func HasOffer(ctx context.Context, provider *providerprocess.Provider, tier environmentv1.Tier, edge *contractv1.EdgeSelection, feature string) (bool, error) {
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

func OfferRepair(ctx context.Context, span *run.Span, provider *providerprocess.Provider, gap Gap, tier environmentv1.Tier, edge *contractv1.EdgeSelection, interactive bool, out io.Writer, in io.Reader) error {
	if gap.IsEmpty() {
		return nil
	}
	if !interactive {
		return gap.refuseMissingWarnStale(tier, span)
	}

	span.Warn(fmt.Sprintf("The %s bootstrap is not what this project needs: %s.", TierName(tier), gap.summary()))
	proceed, err := span.Confirm(func() (bool, error) {
		return terminal.NewPrompt(out, in).Confirm(ctx, fmt.Sprintf("Run `%s` now?", gap.RepairCommand(tier)))
	})
	if err != nil {
		return err
	}
	if !proceed {
		return gap.refuseMissingWarnStale(tier, span)
	}
	_, err = providerprocess.Stream(ctx, provider, "Bootstrap", gap.BootstrapRequest(tier, edge), contractv1connect.ProviderServiceClient.Bootstrap)
	return err
}
