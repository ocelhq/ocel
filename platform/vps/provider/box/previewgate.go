package box

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func (e *Edge) publishPreviewGate(ctx context.Context, publication edge.PreviewGatePublication) error {
	return e.machine.PutPreviewGate(ctx, Surface(publication.State.Slug, publication.State.Tier), publication.Gate)
}

func (e *Edge) PutPreviewGate(ctx context.Context, spec edge.StackSpec) error {
	if spec.Tier != environment.TierPreview {
		return nil
	}
	if err := e.machine.PutPreviewGate(ctx, Surface(spec.Slug, spec.Tier), spec.PreviewGate); err != nil {
		return fmt.Errorf("hand the switchboard the preview gate of %s: %w", spec.Slug, err)
	}
	return nil
}
