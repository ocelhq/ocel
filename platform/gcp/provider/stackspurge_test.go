package gcp

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
)

type codeRunningFront struct {
	edge.Edge
	runsCode bool
	shields  bool
	kind     edge.Kind
}

func (f codeRunningFront) Kind() edge.Kind { return f.kind }
func (f codeRunningFront) Facts() edge.Facts {
	return edge.Facts{RunsCode: f.runsCode, ShieldsOrigin: f.shields}
}

type recordingStacks struct {
	provider.Stacks
	order  *orderLog
	result provider.StackResult
}

func (s recordingStacks) Provision(context.Context, provider.StackSpec, progress.Log) (provider.StackResult, error) {
	s.order.add("provision")
	return s.result, nil
}

type orderLog struct {
	mu    sync.Mutex
	steps []string
}

func (o *orderLog) add(step string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.steps = append(o.steps, step)
}

func adoptTheWorker(t *testing.T, h *offersHarness) {
	t.Helper()
	if err := h.adopt(fullOffers("c1")); err != nil {
		t.Fatal(err)
	}
}

func isrSpec(front edge.Edge, compute provider.Compute, isr *provider.ISRSpec) provider.StackSpec {
	return provider.StackSpec{
		Ref:  provider.StackRef{Project: "shop", Tier: environment.TierProduction},
		Kind: provider.StackApp,
		Edge: front,
		App:  &provider.AppSpec{App: "web", Compute: compute, ISR: isr, StorageCoordinate: cachedRelease()},
	}
}

const storageCoordinate = "shop/prod/web/r1a2b3c4d"

func TestANextDeployBehindTheCodeRunningEdgeHandsItsPurgeSecretToTheRecord(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	order := &orderLog{}
	adoptTheWorker(t, h)
	seed, err := cloudflareorigin.ReadPurgeSeed(t.Context(), h.clients, environment.TierProduction, cloudflareKind)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := stacks{Stacks: recordingStacks{order: order, result: provider.StackResult{}}, p: programming(h)}

	result, err := wrapped.Provision(t.Context(), isrSpec(codeRunningFront{kind: cloudflareKind, runsCode: true}, provider.ComputeServerless, &provider.ISRSpec{}), nil)
	if err != nil {
		t.Fatalf("Provision = %v", err)
	}

	if want := edge.DerivePurgeSecret(seed, storageCoordinate); result.PurgeSecret != want {
		t.Errorf("PurgeSecret = %q, want %q", result.PurgeSecret, want)
	}
	if !slices.Equal(order.steps, []string{"provision"}) {
		t.Errorf("order = %v, want the deploy provisioned and nothing else called", order.steps)
	}
}

func TestADeployNotBehindTheCodeRunningEdgeHandsNoPurgeSecretToTheRecord(t *testing.T) {
	t.Parallel()
	for name, spec := range map[string]provider.StackSpec{
		"the alb front":       isrSpec(codeRunningFront{kind: "alb"}, provider.ComputeServerless, &provider.ISRSpec{}),
		"no front":            isrSpec(nil, provider.ComputeServerless, &provider.ISRSpec{}),
		"container compute":   isrSpec(codeRunningFront{kind: cloudflareKind, runsCode: true}, provider.ComputeContainer, &provider.ISRSpec{}),
		"an app without ISR":  isrSpec(codeRunningFront{kind: cloudflareKind, runsCode: true}, provider.ComputeServerless, nil),
		"a stack with no app": {Edge: codeRunningFront{kind: cloudflareKind, runsCode: true}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newOffersHarness(t)
			order := &orderLog{}
			adoptTheWorker(t, h)
			inner := provider.StackResult{EdgeBundleKey: "k"}
			wrapped := stacks{Stacks: recordingStacks{order: order, result: inner}, p: programming(h)}

			result, err := wrapped.Provision(t.Context(), spec, nil)
			if err != nil {
				t.Fatalf("Provision = %v", err)
			}
			if result.PurgeSecret != "" || result.EdgeBundleKey != "k" {
				t.Errorf("result = %+v, want the inner result unchanged and no purge secret", result)
			}
		})
	}
}

func TestANextDeployBehindAnEdgeItNeverAdoptedIsNotReadyAndDeploysNothing(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	order := &orderLog{}
	wrapped := stacks{Stacks: recordingStacks{order: order}, p: programming(h)}

	_, err := wrapped.Provision(t.Context(), isrSpec(codeRunningFront{kind: cloudflareKind, runsCode: true}, provider.ComputeServerless, &provider.ISRSpec{}), nil)

	if refusalCode(err) != refusal.CodeNotReady {
		t.Errorf("Provision = %v, want a not-ready refusal", err)
	}
	if len(order.steps) != 0 {
		t.Errorf("steps = %v, want nothing deployed", order.steps)
	}
}

func TestANextDeployBehindTheWorkerOnATierWithNoOriginWildcardProvisionsNothing(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	order := &orderLog{}
	adoptTheWorker(t, h)
	spec := isrSpec(codeRunningFront{kind: cloudflareKind, runsCode: true}, provider.ComputeServerless, &provider.ISRSpec{})
	spec.App.Functions = []provider.FunctionSpec{{Name: "web"}}
	wrapped := stacks{Stacks: recordingStacks{order: order}, p: programming(h)}

	_, err := wrapped.Provision(t.Context(), spec, nil)

	if refusalCode(err) != refusal.CodeInvalid || !strings.Contains(err.Error(), "originDomain") {
		t.Errorf("Provision = %v, want an invalid refusal naming originDomain", err)
	}
	if len(order.steps) != 0 {
		t.Errorf("steps %v, want the refusal before anything provisioned", order.steps)
	}
}
