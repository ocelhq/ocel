package bucket

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const consoleBrowser = "https://console.cloud.google.com/storage/browser/"

func (p *Bucket) ListResources(_ context.Context, clients *gcloud.Clients, _ provider.StackRef, binding provider.Binding) ([]provider.PhysicalResource, error) {
	name := binding.Properties[provider.PropertyBucket]
	if name == "" {
		return nil, nil
	}
	return []provider.PhysicalResource{{
		Kind:       string(bootstrap.KindBucket),
		ID:         name,
		Location:   clients.Region,
		ConsoleURL: consoleBrowser + name,
	}}, nil
}
