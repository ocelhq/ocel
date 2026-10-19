package realtime

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func (p *Realtime) ListResources(_ context.Context, clients *gcloud.Clients, ref provider.StackRef, binding provider.Binding) ([]provider.PhysicalResource, error) {
	gateway, err := clients.RealtimeGateway(ref.Project, ref.Name.Env)
	if err != nil {
		return nil, err
	}
	signing, err := clients.RealtimeSigningSecret(ref.Project, ref.Name.Env, realtimeNamespaceOf(binding.Resource, binding.Name))
	if err != nil {
		return nil, err
	}
	keys, err := clients.RealtimeKeysSecret(ref.Tier, ref.Project, ref.Name.Env)
	if err != nil {
		return nil, err
	}
	return []provider.PhysicalResource{
		{Kind: string(bootstrap.KindSecret), ID: signing, Location: bootstrap.LocationGlobal},
		{Kind: string(bootstrap.KindService), ID: gateway, Location: clients.Region},
		{Kind: string(bootstrap.KindTopic), ID: gateway, Location: bootstrap.LocationGlobal},
		{Kind: string(bootstrap.KindSecret), ID: keys, Location: bootstrap.LocationGlobal},
	}, nil
}
