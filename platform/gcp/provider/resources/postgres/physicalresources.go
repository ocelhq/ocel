package postgres

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func (p *Postgres) ListResources(ctx context.Context, clients *gcloud.Clients, ref provider.StackRef, binding provider.Binding) ([]provider.PhysicalResource, error) {
	instances := postgresInstances{clients: clients, records: p.records, ref: ref}
	recorded, found, err := instances.readRecordedPassword(ctx, binding.Name)
	if err != nil || !found || recorded.SetOn == "" {
		return nil, err
	}
	return []provider.PhysicalResource{{Kind: string(bootstrap.KindSQLInstance), ID: recorded.SetOn, Location: clients.Region}}, nil
}
