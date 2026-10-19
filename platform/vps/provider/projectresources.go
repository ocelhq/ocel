package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func (p *Provider) ListProjectResources(_ context.Context, req provider.ProjectResourcesRequest) ([]provider.PhysicalResource, error) {
	namespace, err := provider.NamespaceFromEnv()
	if err != nil {
		return nil, err
	}
	return cloudflare.ListProjectResources(namespace.String(), req), nil
}
