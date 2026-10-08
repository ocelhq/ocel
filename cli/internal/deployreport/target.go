package deployreport

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func ReadTarget(ctx context.Context, provider *providerprocess.Provider) string {
	var described *contractv1.DescribeConnectorTargetResponse
	err := provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		described, err = client.DescribeConnectorTarget(ctx, &contractv1.DescribeConnectorTargetRequest{})
		return err
	})
	if err != nil {
		return ""
	}
	return described.GetTargetFingerprint()
}
