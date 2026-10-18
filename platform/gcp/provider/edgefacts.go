package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
)

type edgeFacts struct {
	originBase  string
	purgeSecret string
}

type readEdgeFactsKey struct{}

type stackEdgeFacts struct {
	ref   provider.StackRef
	facts edgeFacts
}

func withEdgeFacts(ctx context.Context, spec provider.StackSpec, facts edgeFacts) context.Context {
	return context.WithValue(ctx, readEdgeFactsKey{}, stackEdgeFacts{ref: spec.Ref, facts: facts})
}

func (p *Provider) readEdgeFacts(ctx context.Context, spec provider.StackSpec) (edgeFacts, error) {
	if read, found := ctx.Value(readEdgeFactsKey{}).(stackEdgeFacts); found && read.ref == spec.Ref {
		return read.facts, nil
	}
	originBase, err := p.readOriginBase(ctx, spec)
	if err != nil {
		return edgeFacts{}, err
	}
	purgeSecret, err := p.readEdgePurgeSecret(ctx, spec)
	if err != nil {
		return edgeFacts{}, err
	}
	return edgeFacts{originBase: originBase, purgeSecret: purgeSecret}, nil
}
