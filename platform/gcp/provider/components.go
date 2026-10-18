package gcp

import (
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/kv"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/realtime"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
)

func (p *Provider) components() component.Registry {
	return component.Registry{
		postgres.New(p.openClients, p.KeyValues(), p.Cipher()).Component(),
		bucket.New(p.openClients).Component(),
		kv.New(p.openClients, gcloud.NewMemorystore(p.endpoint, p.tokens)).Component(),
		p.topicHooks().Component(),
		p.realtimeHooks().Component(),
	}
}

func (p *Provider) topicHooks() *topic.Topic { return topic.New(p.openClients, p.KeyValues()) }

func (p *Provider) realtimeHooks() *realtime.Realtime {
	return realtime.New(p.openClients, p.KeyValues(), p.services(), p.pushBinary, p.emulated())
}

func (p *Provider) resourceHooks() resources.Hooks {
	hooks := p.components().Hooks()

	return p.computeHooks(hooks)
}
