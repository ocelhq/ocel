package preflight

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func ResolveComputesFromProvider(ctx context.Context, prov *providerclient.Provider, cfg *project.Project) (*project.Project, error) {
	var resp *contractv1.PreflightResponse
	err := prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		resp, err = client.Preflight(ctx, &contractv1.PreflightRequest{Edge: cfg.EdgeSelection()})
		return err
	})
	if err != nil {
		return nil, err
	}
	return cfg.ResolveComputes(resp.GetComputes(), prov.Name())
}
