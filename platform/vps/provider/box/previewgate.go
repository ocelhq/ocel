package box

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
)

func (e *Edge) publishPreviewGate(ctx context.Context, publication edge.PreviewGatePublication) error {
	return e.machine.PutPreviewGate(ctx, Surface(publication.State.Slug, publication.State.Tier), publication.Gate)
}
