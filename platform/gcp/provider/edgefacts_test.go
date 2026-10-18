package gcp

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

type edgeFactsReadingStacks struct {
	provider.Stacks
	read func(ctx context.Context, spec provider.StackSpec) (edgeFacts, error)
	seen *edgeFacts
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
	seed, err := readPurgeSeed(t.Context(), h.clients, environment.TierProduction, cloudflareKind)
	if err != nil {
		t.Fatal(err)
	}
	p := programming(h)
	var seen edgeFacts
	wrapped := stacks{Stacks: edgeFactsReadingStacks{seen: &seen, read: func(ctx context.Context, spec provider.StackSpec) (edgeFacts, error) {
		h.secrets.mu.Lock()
		clear(h.secrets.secrets)
		h.secrets.mu.Unlock()
		return p.readEdgeFacts(ctx, spec)
	}}, p: p}

	_, err = wrapped.Provision(t.Context(), isrSpec(codeRunningFront{kind: cloudflareKind, runsCode: true}, provider.ComputeServerless, &provider.ISRSpec{Prefix: isrPrefix}), nil)
	if err != nil {
		t.Fatalf("Provision = %v, want the functions handed what the deploy already read", err)
	}
	if want := cloudflare.DerivePurgeSecret(seed, isrPrefix); seen.purgeSecret != want {
		t.Errorf("purge secret the functions saw = %q, want %q", seen.purgeSecret, want)
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
	ctx := withEdgeFacts(t.Context(), other, edgeFacts{purgeSecret: "not-this-stacks"})

	facts, err := p.readEdgeFacts(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if facts.purgeSecret == "not-this-stacks" {
		t.Errorf("readEdgeFacts handed stack shop the facts read for stack blog")
	}
}
