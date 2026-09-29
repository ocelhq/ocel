package readiness

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"

	"google.golang.org/protobuf/proto"
)

type Preflight struct {
	Project  *project.Project
	Response *contractv1.PreflightResponse
}

func Read(ctx context.Context, provider *providerprocess.Provider, cfg *project.Project, req Request) (Preflight, error) {
	sent := newPreflightRequest(cfg, req)
	resp, err := preflight(ctx, provider, sent)
	if err != nil {
		return Preflight{}, err
	}
	if len(resp.GetCredentialProblems()) > 0 {
		return Preflight{Response: resp}, nil
	}
	resolved, err := cfg.ResolveComputes(resp.GetComputes(), provider.Name())
	if err != nil {
		return Preflight{}, err
	}
	resent := newPreflightRequest(resolved, req)
	if isAskingTheSame(sent, resent) {
		return Preflight{Project: resolved, Response: resp}, nil
	}
	resp, err = preflight(ctx, provider, resent)
	if err != nil {
		return Preflight{}, err
	}
	return Preflight{Project: resolved, Response: resp}, nil
}

func ResolveComputes(ctx context.Context, provider *providerprocess.Provider, cfg *project.Project) (*project.Project, error) {
	resp, err := preflight(ctx, provider, &contractv1.PreflightRequest{Edge: cfg.EdgeSelection()})
	if err != nil {
		return nil, err
	}
	return cfg.ResolveComputes(resp.GetComputes(), provider.Name())
}

func preflight(ctx context.Context, provider *providerprocess.Provider, req *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	var resp *contractv1.PreflightResponse
	err := provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		resp, err = client.Preflight(ctx, req)
		return err
	})
	return resp, err
}

func isAskingTheSame(sent, resent *contractv1.PreflightRequest) bool {
	return slices.Equal(sent.GetFrameworks(), resent.GetFrameworks()) &&
		slices.EqualFunc(sent.GetContainers(), resent.GetContainers(), func(a, b *contractv1.ContainerApp) bool {
			return proto.Equal(a, b)
		})
}
