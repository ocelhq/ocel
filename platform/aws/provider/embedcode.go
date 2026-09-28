package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) EmbedCode(ctx context.Context, function string, artifact provider.ArtifactRef, progress progress.Progress) error {
	return p.stacks.EmbedCode(ctx, function, artifact, progress)
}
