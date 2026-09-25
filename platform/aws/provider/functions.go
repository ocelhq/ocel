package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) WarmFunctions(ctx context.Context, targets []string, report providerkit.Reporter) error {
	return p.releases.Warm(ctx, targets, report)
}

func (p *Provider) EmbedCode(ctx context.Context, function string, artifact providerkit.ArtifactRef, report providerkit.Reporter) error {
	return p.releases.EmbedCode(ctx, function, artifact, report)
}
