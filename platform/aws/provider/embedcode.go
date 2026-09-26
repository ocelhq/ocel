package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) EmbedCode(ctx context.Context, function string, artifact providerkit.ArtifactRef, progress edge.Progress) error {
	return p.stacks.EmbedCode(ctx, function, artifact, progress)
}
