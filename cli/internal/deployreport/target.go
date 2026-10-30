package deployreport

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func ReadTarget(ctx context.Context, provider *providerprocess.Provider) (string, error) {
	var described *contractv1.DescribeConnectorTargetResponse
	err := provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		described, err = client.DescribeConnectorTarget(ctx, &contractv1.DescribeConnectorTargetRequest{})
		return err
	})
	return targetFrom(described, err)
}

func targetFrom(described *contractv1.DescribeConnectorTargetResponse, err error) (string, error) {
	switch {
	case connect.CodeOf(err) == connect.CodeUnimplemented:
		return "", nil
	case err != nil:
		return "", fmt.Errorf("the provider named no target for the deployment record: %w", err)
	}
	return described.GetTargetFingerprint(), nil
}
