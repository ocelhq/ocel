package gcp

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
)

func (p *Provider) ListStackResources(ctx context.Context, ref provider.StackRef, recorded provider.StackResult) ([]provider.PhysicalResource, error) {
	clients, err := p.resolveClients(ctx)
	if err != nil {
		return nil, err
	}
	var listed []provider.PhysicalResource
	for _, function := range recorded.Functions {
		listed = append(listed, provider.PhysicalResource{Kind: string(bootstrap.KindService), ID: function.Physical, Location: clients.Region})
	}
	for _, container := range recorded.Containers {
		listed = append(listed, provider.PhysicalResource{Kind: string(bootstrap.KindService), ID: container.Physical, Location: clients.Region})
	}
	if !ref.Name.IsInfra() && len(recorded.Functions)+len(recorded.Containers) > 0 {
		secrets, err := release.ReadValueSecrets(ctx, p.KeyValues(), ref.Tier, clients.AppAccount(ref.Project, ref.Name.Env, ref.Name.App))
		if err != nil {
			return nil, err
		}
		for _, secret := range secrets {
			listed = append(listed, provider.PhysicalResource{Kind: string(bootstrap.KindSecret), ID: secret, Location: bootstrap.LocationGlobal})
		}
	}
	registry := p.components()
	for _, binding := range recorded.Bindings {
		served, found := registry.For(binding.Type)
		if !found || served.ListResources == nil {
			return nil, refusal.Refuse(refusal.CodeNotReady,
				"%s records the %s %s, and this provider names no resources for a %s", ref.Name, binding.Type, binding.Name, binding.Type)
		}
		held, err := served.ListResources(ctx, clients, ref, binding)
		if err != nil {
			return nil, err
		}
		for _, resource := range held {
			if !slices.Contains(listed, resource) {
				listed = append(listed, resource)
			}
		}
	}
	return listed, nil
}
