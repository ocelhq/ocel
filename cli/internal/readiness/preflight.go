package readiness

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func Read(ctx context.Context, provider *providerprocess.Provider, cfg *project.Project, req Request) (*contractv1.PreflightResponse, error) {
	sent := newPreflightRequest(cfg, req)
	resp, err := preflight(ctx, provider, sent)
	if err != nil {
		return nil, err
	}
	resent, differs := resolvedPreflightRequest(cfg, req, provider.Name(), sent, resp)
	if !differs {
		return resp, nil
	}
	return preflight(ctx, provider, resent)
}

func ResolveComputes(ctx context.Context, provider *providerprocess.Provider, cfg *project.Project) (*project.Project, error) {
	resp, err := preflight(ctx, provider, &contractv1.PreflightRequest{Edge: cfg.EdgeSelection()})
	if err != nil {
		return nil, err
	}
	return cfg.ResolveComputes(resp.GetComputes(), provider.Name())
}

func ReadContainerArchs(ctx context.Context, provider *providerprocess.Provider, resolved *project.Project, announced map[string]string) (map[string]string, error) {
	named := containers(resolved)
	missing := slices.ContainsFunc(named, func(container *contractv1.ContainerApp) bool {
		_, ok := announced[container.GetApp()]
		return !ok
	})
	if !missing {
		return announced, nil
	}
	resp, err := preflight(ctx, provider, &contractv1.PreflightRequest{Edge: resolved.EdgeSelection(), Containers: named})
	if err != nil {
		return nil, err
	}
	return resp.GetContainerArchs(), nil
}

func preflight(ctx context.Context, provider *providerprocess.Provider, req *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	var resp *contractv1.PreflightResponse
	err := provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		resp, err = client.Preflight(ctx, req)
		return err
	})
	return resp, err
}

func resolvedPreflightRequest(cfg *project.Project, req Request, vendor string, sent *contractv1.PreflightRequest, resp *contractv1.PreflightResponse) (*contractv1.PreflightRequest, bool) {
	if len(resp.GetCredentialProblems()) > 0 {
		return nil, false
	}
	resolved, err := cfg.ResolveComputes(resp.GetComputes(), vendor)
	if err != nil {
		return nil, false
	}
	resent := newPreflightRequest(resolved, req)
	if slices.Equal(resent.GetFrameworks(), sent.GetFrameworks()) {
		return nil, false
	}
	return resent, true
}
