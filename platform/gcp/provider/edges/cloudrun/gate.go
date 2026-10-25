package cloudrun

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
)

func (e *Edge) publishGate(ctx context.Context, publication edge.GatePublication) error {
	return e.gates.Publish(ctx, publication.State.Tier, publication.State.Slug, publication.Gate)
}
