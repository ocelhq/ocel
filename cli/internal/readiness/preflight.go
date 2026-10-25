package readiness

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Preflight struct {
	Project  *project.Project
	Response *contractv1.PreflightResponse
	Registry *contractv1.ImageRegistry
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
	return Preflight{Project: resolved, Response: resp, Registry: req.Registry}, nil
}

func ResolveComputes(provider *providerprocess.Provider, cfg *project.Project) (*project.Project, error) {
	resolved, err := cfg.ResolveComputes(provider.Facts().GetComputes(), provider.Name())
	if err != nil {
		return nil, err
	}
	return build.ResolveNextContainerBuilds(resolved, provider.Facts())
}

func preflight(ctx context.Context, provider *providerprocess.Provider, req *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	return providerprocess.Preflight(ctx, provider, req)
}
