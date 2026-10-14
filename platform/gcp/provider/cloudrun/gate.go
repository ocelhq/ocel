package cloudrun

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
)

func (e *Edge) publishGate(ctx context.Context, publication edge.GatePublication) error {
	if publication.Say != nil {
		publication.Say("Writing the " + string(publication.State.Tier) + " gate of " + publication.State.Slug + " to the secret its Cloud Run services mount")
	}
	return e.gates.Publish(ctx, publication.State.Tier, publication.State.Slug, publication.Gate)
}
