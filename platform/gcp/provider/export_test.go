package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

var IngressFor = ingressFor

const (
	TasksPart       = tasksPart
	NetworkPart     = networkPart
	TagDatabasePart = tagDatabasePart
)

func AddParts(ctx context.Context, p *Provider, tier environment.Tier, names ...string) error {
	return p.addParts(ctx, provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: tier, Slug: "live-suite"}}, names)
}

func PartsRolePermissions(namespace provider.Namespace, project string) []string {
	return newPartsRole(Names{namespace: namespace, project: project}).permissions
}
