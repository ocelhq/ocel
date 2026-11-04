package control

import (
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/kv"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/realtime"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
)

func (p *Provider) listings() component.Registry {
	return component.Registry{
		postgres.New(p.OpenClients, p.KeyValues(), p.Cipher(), p.SkipChecks).Listing(),
		bucket.New(p.OpenClients, p.KeyValues()).Listing(),
		kv.New(p.OpenClients, p.KeyValues(), p.SkipChecks).Listing(),
		topic.New(p.OpenClients, p.KeyValues()).Listing(),
		realtime.New(p.OpenClients, p.KeyValues(), p.Services(), nil, p.ContainerEndpoint()).Listing(),
	}
}
