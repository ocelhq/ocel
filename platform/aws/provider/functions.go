package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) WarmFunctions(ctx context.Context, targets []string, progress providerkit.Progress) error {
	return p.releases.Warm(ctx, targets, progress)
}

func (p *Provider) EmbedCode(ctx context.Context, function string, artifact providerkit.ArtifactRef, progress providerkit.Progress) error {
	return p.releases.EmbedCode(ctx, function, artifact, progress)
}
