package gcp

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func programming(h *offersHarness) *Provider {
	return &Provider{namespace: "ocel", resolved: h.clients, records: h.records}
}

func entryRequest(tier environment.Tier, slug string) provider.EdgeProgramRequest {
	return provider.EdgeProgramRequest{
		Tier: tier, Kind: cloudflareKind, Slug: slug, Env: "main",
		PreviewBaseDomain: "preview.example.com", PreviewKey: "k",
		Entry: edge.WorkerModule{Name: "entry.mjs", ContentType: "application/javascript+module", Content: []byte("export default {}")},
	}
}

func TestTheEntryWorkerProgramReachesGCPThroughItsClientCertificateAndHoldsNoAWSKeys(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adopt(fullOffers("c1", "c2"), map[string]string{"cacheBucket": "b"}); err != nil {
		t.Fatal(err)
	}

	program, err := programming(h).ProgramEdge(t.Context(), entryRequest(environment.TierProduction, "shop"))
	if err != nil {
		t.Fatalf("ProgramEdge = %v", err)
	}

	want, err := cloudflare.EntryProgram{
		Tier: environment.TierProduction, Entry: entryRequest(environment.TierProduction, "shop").Entry,
		Namespace: "ocel", Slug: "shop", Env: "main", PreviewBaseDomain: "preview.example.com", PreviewKey: "k",
		Origin:                   cloudflare.OriginBindings{ClientCertificate: "m1"},
		StoreScriptName:          "ocel-deployments-store",
		StoreEndpoint:            "https://s.example.workers.dev",
		StoreBootstrapCredential: "c1",
		ISRWriterScriptName:      "ocel-isr-writer",
		Values:                   map[string]string{"cacheBucket": "b"},
	}.Build()
	if err != nil {
		t.Fatal(err)
	}
	if program.Spec.Name != want.Spec.Name || program.Spec.StoreScriptName != "ocel-deployments-store" ||
		program.Spec.StoreEndpoint != "https://s.example.workers.dev" || program.Spec.BootstrapCredential != "c1" ||
		program.Spec.ISRWriterScriptName != "ocel-isr-writer" {
		t.Errorf("program = %+v, want %+v", program.Spec, want.Spec)
	}
	if got := program.Spec.Worker.ClientCertificates[edge.OriginClientCertificateBinding]; got != "m1" {
		t.Errorf("client certificate binding = %q, want m1", got)
	}
	if _, keyed := program.Spec.Worker.Variables[edge.EdgeAccessKeyIDVar]; keyed {
		t.Error("the worker holds an AWS access key id")
	}
	if _, keyed := program.Spec.Worker.Secrets[edge.EdgeSecretKeyVar]; keyed {
		t.Error("the worker holds an AWS secret key")
	}
	if program.Values["cacheBucket"] != "b" {
		t.Errorf("values = %v, want the adopted values", program.Values)
	}
}

func TestTheSharedPreviewEntryProgramReadsEveryDeploymentThroughTheAdoptedStore(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adoptIn(environment.TierPreview, fullOffers("c1", "c2"), nil); err != nil {
		t.Fatal(err)
	}

	program, err := programming(h).ProgramEdge(t.Context(), entryRequest(environment.TierPreview, ""))
	if err != nil {
		t.Fatalf("ProgramEdge = %v", err)
	}
	if program.Spec.StoreScriptName != "ocel-deployments-store" || len(program.Spec.Worker.Services) == 0 {
		t.Errorf("program = %+v, want the store bound as a service", program.Spec)
	}
}

func TestProgrammingAnEntryWorkerBeforeBootstrapIsNotReady(t *testing.T) {
	t.Parallel()
	_, err := programming(newOffersHarness(t)).ProgramEdge(t.Context(), entryRequest(environment.TierProduction, "shop"))
	if refusalCode(err) != refusal.CodeNotReady || !strings.Contains(err.Error(), "ocel bootstrap production") {
		t.Errorf("ProgramEdge = %v, want a not-ready refusal naming ocel bootstrap production", err)
	}
}

func TestProgrammingAnEntryWorkerWithoutAnAdoptedClientCertificateIsNotReady(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adopt([]edge.Offer{storeOffer("c1"), writerOffer("c2")}, nil); err != nil {
		t.Fatal(err)
	}
	_, err := programming(h).ProgramEdge(t.Context(), entryRequest(environment.TierProduction, "shop"))
	if refusalCode(err) != refusal.CodeNotReady || !strings.Contains(err.Error(), "client certificate") {
		t.Errorf("ProgramEdge = %v, want a not-ready refusal about the client certificate", err)
	}
}

func TestTheProviderHandsTheEntryWorkerProgramToItsHooks(t *testing.T) {
	t.Parallel()
	if (&Provider{}).Hooks().ProgramEdge == nil {
		t.Error("Hooks().ProgramEdge is unset, so no edge that runs code gets a program")
	}
}
