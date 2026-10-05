package readiness

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type Preflight struct {
	Project  *project.Project
	Response *contractv1.PreflightResponse
}

func Read(ctx context.Context, provider *providerprocess.Provider, cfg *project.Project, req Request) (Preflight, error) {
	resolved, err := ResolveComputes(provider, cfg)
	if err != nil {
		return Preflight{}, err
	}
	resp, err := preflight(ctx, provider, newPreflightRequest(resolved, req))
	if err != nil {
		return Preflight{}, err
	}
	return Preflight{Project: resolved, Response: resp}, nil
}

func ResolveComputes(provider *providerprocess.Provider, cfg *project.Project) (*project.Project, error) {
	return cfg.ResolveComputes(provider.Facts().GetComputes(), provider.Name())
}

func preflight(ctx context.Context, provider *providerprocess.Provider, req *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	var resp *contractv1.PreflightResponse
	err := provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		resp, err = client.Preflight(ctx, req)
		return err
	})
	return resp, err
}
