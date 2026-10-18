package release

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/next"
)

type EdgeFacts struct {
	OriginBase  string
	PurgeSecret string
}

type readEdgeFactsKey struct{}

type stackEdgeFacts struct {
	ref   provider.StackRef
	facts EdgeFacts
}

func WithEdgeFacts(ctx context.Context, spec provider.StackSpec, facts EdgeFacts) context.Context {
	return context.WithValue(ctx, readEdgeFactsKey{}, stackEdgeFacts{ref: spec.Ref, facts: facts})
}

func (p *Release) ReadEdgeFacts(ctx context.Context, spec provider.StackSpec) (EdgeFacts, error) {
	if read, found := ctx.Value(readEdgeFactsKey{}).(stackEdgeFacts); found && read.ref == spec.Ref {
		return read.facts, nil
	}
	originBase, err := p.Origins.ReadBase(ctx, spec)
	if err != nil {
		return EdgeFacts{}, err
	}
	purgeSecret, err := p.readEdgePurgeSecret(ctx, spec)
	if err != nil {
		return EdgeFacts{}, err
	}
	return EdgeFacts{OriginBase: originBase, PurgeSecret: purgeSecret}, nil
}

func (p *Release) readEdgePurgeSecret(ctx context.Context, spec provider.StackSpec) (string, error) {
	if !next.SignsEdgePurges(spec) {
		return "", nil
	}
	return p.Offers.ReadPurgeSecret(ctx, spec.Ref.Tier, spec.Edge.Kind(), spec.App.ISR.Prefix)
}
