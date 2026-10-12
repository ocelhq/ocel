package cloudrun

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
)

func (e *Edge) publishPreviewGate(ctx context.Context, publication edge.PreviewGatePublication) error {
	if publication.Say != nil {
		publication.Say("Writing the preview gate of " + publication.State.Slug + " to the secret its Cloud Run services mount")
	}
	return e.gates.Publish(ctx, publication.State.Slug, publication.Gate)
}
