package gcp

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
)

type edgeFactsReadingStacks struct {
	provider.Stacks
	read func(ctx context.Context, spec provider.StackSpec) (release.EdgeFacts, error)
	seen *release.EdgeFacts
}

func (s edgeFactsReadingStacks) Provision(ctx context.Context, spec provider.StackSpec, _ progress.Log) (provider.StackResult, error) {
	facts, err := s.read(ctx, spec)
	*s.seen = facts
	return provider.StackResult{}, err
}

func TestTheFunctionsOfADeployReadTheEdgeFactsTheDeployReadAndNotTheStoreAgain(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	adoptTheWorker(t, h)
	seed, err := cloudflareorigin.ReadPurgeSeed(t.Context(), h.clients, environment.TierProduction, cloudflareKind)
	if err != nil {
		t.Fatal(err)
	}
	p := programming(h)
	var seen release.EdgeFacts
	wrapped := stacks{Stacks: edgeFactsReadingStacks{seen: &seen, read: func(ctx context.Context, spec provider.StackSpec) (release.EdgeFacts, error) {
		h.secrets.Mu.Lock()
		clear(h.secrets.Secrets)
		h.secrets.Mu.Unlock()
		return p.release().ReadEdgeFacts(ctx, spec)
	}}, p: p}

	_, err = wrapped.Provision(t.Context(), isrSpec(codeRunningFront{kind: cloudflareKind, runsCode: true}, provider.ComputeServerless, &provider.ISRSpec{Prefix: isrPrefix}), nil)
	if err != nil {
		t.Fatalf("Provision = %v, want the functions handed what the deploy already read", err)
	}
	if want := edge.DerivePurgeSecret(seed, isrPrefix); seen.PurgeSecret != want {
		t.Errorf("purge secret the functions saw = %q, want %q", seen.PurgeSecret, want)
	}
}

func TestEdgeFactsReadForOneStackAreNotHandedToAnother(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	adoptTheWorker(t, h)
	p := programming(h)
	spec := isrSpec(codeRunningFront{kind: cloudflareKind, runsCode: true}, provider.ComputeServerless, &provider.ISRSpec{Prefix: isrPrefix})
	other := spec
	other.Ref.Project = "blog"
	ctx := release.WithEdgeFacts(t.Context(), other, release.EdgeFacts{PurgeSecret: "not-this-stacks"})

	facts, err := p.release().ReadEdgeFacts(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if facts.PurgeSecret == "not-this-stacks" {
		t.Errorf("readEdgeFacts handed stack shop the facts read for stack blog")
	}
}
