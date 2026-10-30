package gcp

import (
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/kv"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/realtime"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
)

func (p *Provider) Components() component.Registry {
	return component.Registry{
		postgres.New(p.OpenClients, p.KeyValues(), p.Cipher(), p.SkipChecks).Component(),
		bucket.New(p.OpenClients, p.KeyValues()).Component(),
		kv.New(p.OpenClients, p.KeyValues(), p.SkipChecks).Component(),
		p.TopicHooks().Component(),
		realtime.New(p.OpenClients, p.KeyValues(), p.Services(), p.pushBinary, p.ContainerEndpoint()).Component(),
	}
}

func (p *Provider) TopicHooks() *topic.Topic {
	return topic.New(p.OpenClients, p.KeyValues())
}

func (p *Provider) resourceHooks() resources.Hooks {
	registry := p.Components()
	if p.transforms != nil {
		registry = registry.WithEvaluationRequired()
	}
	hooks := registry.Hooks()
	hooks.ListResources = p.ListStackResources

	return p.computeHooks(hooks)
}
