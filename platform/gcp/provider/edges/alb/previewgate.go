package alb

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
)

func (e *Edge) publishPreviewGate(ctx context.Context, publication edge.PreviewGatePublication) error {
	if publication.Say != nil {
		publication.Say("Rolling the preview gate of " + publication.State.Slug + " out to the load balancer's gate plugin, which takes a few minutes")
	}
	opened, err := e.Open(publication.State)
	if err != nil {
		return err
	}
	return opened.(*stack).syncGate(ctx, edge.StackSpec{
		Slug:        publication.State.Slug,
		Tier:        publication.State.Tier,
		PreviewGate: publication.Gate,
		Warn:        publication.Say,
	})
}
