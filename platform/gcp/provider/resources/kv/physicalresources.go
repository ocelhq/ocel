package kv

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func (p *KV) ListResources(_ context.Context, clients *gcloud.Clients, ref provider.StackRef, binding provider.Binding) ([]provider.PhysicalResource, error) {
	instance, err := clients.KVInstance(ref.Project, ref.Name.Env, binding.Name)
	if err != nil {
		return nil, err
	}
	return []provider.PhysicalResource{{Kind: string(bootstrap.KindMemorystoreInstance), ID: instance, Location: clients.Region}}, nil
}
