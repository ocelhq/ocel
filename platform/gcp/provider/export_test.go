package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/task"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
)

var IngressFor = run.IngressFor

const (
	TasksPart       = task.PartName
	NetworkPart     = network.PartName
	TagDatabasePart = tagDatabasePart
)

func AddParts(ctx context.Context, p *Provider, tier environment.Tier, names ...string) error {
	return p.addParts(ctx, provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: tier, Slug: "live-suite"}}, names)
}

func PartsRolePermissions(namespace provider.Namespace, project string) []string {
	return bootstrap.NewPartsRole(names.New(namespace, project)).Permissions
}

func (p *Provider) TopicHooks() *topic.Topic { return p.topicHooks() }
