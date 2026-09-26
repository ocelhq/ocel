package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) EmbedCode(ctx context.Context, function string, artifact providerkit.ArtifactRef, progress providerkit.Progress) error {
	return p.stacks.EmbedCode(ctx, function, artifact, progress)
}
