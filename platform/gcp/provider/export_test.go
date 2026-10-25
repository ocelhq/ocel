package gcp

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/functions"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
)

var IngressFor = run.IngressFor

const NetworkPart = network.PartName

func MakeProjectDatabase(ctx context.Context, p *Provider, tier environment.Tier, project string) error {
	return p.ensureProjectDatabase(ctx, provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: tier, Slug: project}})
}

func AddParts(ctx context.Context, p *Provider, tier environment.Tier, names ...string) error {
	return p.addParts(ctx, provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: tier, Slug: "live-suite"}}, names)
}

func (p *Provider) TopicHooks() *topic.Topic { return p.topicHooks() }

func (p *Provider) FunctionImages() functions.Images { return p.functionImages() }

func (p *Provider) PullingImagesWith(pull func(ctx context.Context, ref string) (v1.Image, error)) *Provider {
	p.images = &run.Images{Pull: pull}
	return p
}
