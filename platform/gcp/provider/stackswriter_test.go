package gcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

type codeRunningFront struct {
	edge.Edge
	runsCode bool
	kind     edge.Kind
}

func (f codeRunningFront) Kind() edge.Kind   { return f.kind }
func (f codeRunningFront) Facts() edge.Facts { return edge.Facts{RunsCode: f.runsCode} }

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

type writerRequest struct {
	path, authorization string
	body                map[string]string
}

type isrWriterServer struct {
	mu       sync.Mutex
	requests []writerRequest
}

func serveISRWriter(t *testing.T, order *orderLog) (*isrWriterServer, string) {
	t.Helper()
	s := &isrWriterServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.requests = append(s.requests, writerRequest{path: r.URL.Path, authorization: r.Header.Get("Authorization"), body: body})
		s.mu.Unlock()
		order.add("initialize")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return s, server.URL
}

func adoptWriterAt(t *testing.T, h *offersHarness, endpoint string) {
	t.Helper()
	writer := writerOffer("c2")
	writer.Values[edge.OfferKeyISRWriterEndpoint] = endpoint
	if err := h.adopt([]edge.Offer{storeOffer("c1"), writer, certificateOffer()}, nil); err != nil {
		t.Fatal(err)
	}
}

func isrSpec(front edge.Edge, compute provider.Compute, isr *provider.ISRSpec) provider.StackSpec {
	return provider.StackSpec{
		Ref:  provider.StackRef{Project: "shop", Tier: environment.TierProduction},
		Kind: provider.StackApp,
		Edge: front,
		App:  &provider.AppSpec{App: "web", Compute: compute, ISR: isr},
	}
}

const isrPrefix = "p/shop/main/web/r1/isr"

func TestANextDeployBehindTheCodeRunningEdgeRegistersItsISRPrefixAndHandsItsWriteSecretToTheRecord(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	order := &orderLog{}
	writer, endpoint := serveISRWriter(t, order)
	adoptWriterAt(t, h, endpoint)
	seed, err := readISRWriterSeed(t.Context(), h.clients, environment.TierProduction, cloudflareKind)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := stacks{Stacks: recordingStacks{order: order, result: provider.StackResult{}}, p: programming(h)}

	result, err := wrapped.Provision(t.Context(), isrSpec(codeRunningFront{kind: cloudflareKind, runsCode: true}, provider.ComputeServerless, &provider.ISRSpec{Prefix: isrPrefix}), nil)
	if err != nil {
		t.Fatalf("Provision = %v", err)
	}

	secret := cloudflare.DeriveISRWriteSecret(seed, isrPrefix)
	if result.ISRWriteSecret != secret {
		t.Errorf("ISRWriteSecret = %q, want %q", result.ISRWriteSecret, secret)
	}
	if len(writer.requests) != 1 {
		t.Fatalf("%d writer requests, want 1", len(writer.requests))
	}
	got := writer.requests[0]
	if got.path != "/"+isrPrefix+"/initialize" || got.authorization != "Bearer c2" {
		t.Errorf("request = %+v, want POST /%s/initialize with the writer credential", got, isrPrefix)
	}
	if got.body["secretHash"] == "" || got.body["secretHash"] == secret {
		t.Errorf("body = %v, want the SHA-256 of the write secret and never the secret", got.body)
	}
	if !slices.Equal(order.steps, []string{"initialize", "provision"}) {
		t.Errorf("order = %v, want the prefix registered before the deploy provisions", order.steps)
	}
}

func TestADeployNotBehindTheCodeRunningEdgeTouchesNoISRWriter(t *testing.T) {
	t.Parallel()
	for name, spec := range map[string]provider.StackSpec{
		"the alb front":       isrSpec(codeRunningFront{kind: "alb"}, provider.ComputeServerless, &provider.ISRSpec{Prefix: isrPrefix}),
		"no front":            isrSpec(nil, provider.ComputeServerless, &provider.ISRSpec{Prefix: isrPrefix}),
		"container compute":   isrSpec(codeRunningFront{kind: cloudflareKind, runsCode: true}, provider.ComputeContainer, &provider.ISRSpec{Prefix: isrPrefix}),
		"an app without ISR":  isrSpec(codeRunningFront{kind: cloudflareKind, runsCode: true}, provider.ComputeServerless, nil),
		"a stack with no app": {Edge: codeRunningFront{kind: cloudflareKind, runsCode: true}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newOffersHarness(t)
			order := &orderLog{}
			writer, endpoint := serveISRWriter(t, order)
			adoptWriterAt(t, h, endpoint)
			inner := provider.StackResult{EdgeBundleKey: "k"}
			wrapped := stacks{Stacks: recordingStacks{order: order, result: inner}, p: programming(h)}

			result, err := wrapped.Provision(t.Context(), spec, nil)
			if err != nil {
				t.Fatalf("Provision = %v", err)
			}
			if len(writer.requests) != 0 || result.ISRWriteSecret != "" || result.EdgeBundleKey != "k" {
				t.Errorf("requests = %d, result = %+v, want no writer call and the inner result unchanged", len(writer.requests), result)
			}
		})
	}
}

func TestANextDeployBehindAnEdgeItNeverAdoptedIsNotReadyAndDeploysNothing(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	order := &orderLog{}
	wrapped := stacks{Stacks: recordingStacks{order: order}, p: programming(h)}

	_, err := wrapped.Provision(t.Context(), isrSpec(codeRunningFront{kind: cloudflareKind, runsCode: true}, provider.ComputeServerless, &provider.ISRSpec{Prefix: isrPrefix}), nil)

	if refusalCode(err) != refusal.CodeNotReady {
		t.Errorf("Provision = %v, want a not-ready refusal", err)
	}
	if len(order.steps) != 0 {
		t.Errorf("steps = %v, want nothing deployed", order.steps)
	}
}
