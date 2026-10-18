package gcp

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func programming(h *offersHarness) *Provider {
	return &Provider{namespace: "ocel", resolved: h.clients, records: h.records}
}

func (h *offersHarness) keyAssets(t *testing.T, tier environment.Tier) {
	t.Helper()
	credentials, err := readEdgeCredentials(t.Context(), h.clients, tier, cloudflareKind)
	if err != nil {
		t.Fatal(err)
	}
	credentials.AssetStoreAccessKeyID, credentials.AssetStoreSecretAccessKey = "GOOG1EREADER", "reader-secret"
	if err := writeEdgeCredentials(t.Context(), h.clients, tier, cloudflareKind, credentials); err != nil {
		t.Fatal(err)
	}
}

func entryRequest(tier environment.Tier) provider.EdgeProgramRequest {
	return provider.EdgeProgramRequest{Tier: tier, Kind: cloudflareKind}
}

func TestTheEntryWorkerProgramReachesGCPThroughItsClientCertificateAndHoldsNoAWSKeys(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adopt(fullOffers("c1")); err != nil {
		t.Fatal(err)
	}
	h.keyAssets(t, environment.TierProduction)

	program, err := programming(h).ProgramEdge(t.Context(), entryRequest(environment.TierProduction))
	if err != nil {
		t.Fatalf("ProgramEdge = %v", err)
	}

	wantStore := edge.ReleasesStore{Name: "ocel-releases-store", Endpoint: "https://s.example.workers.dev", BootstrapCredential: "c1"}
	if program.ReleasesStore != wantStore {
		t.Errorf("releases store = %+v, want %+v", program.ReleasesStore, wantStore)
	}
	if want := (edge.Reach{Kind: edge.ReachClientCertificate, Certificate: "m1"}); program.Origin.Reach != want {
		t.Errorf("reach = %+v, want %+v: GCP is reached by the client certificate alone, with no signing keys", program.Origin.Reach, want)
	}
	if want := (edge.Throttle{Status: 429, BodyContains: "no available instance"}); program.Origin.Throttle != want {
		t.Errorf("throttle = %+v, want %+v: Cloud Run names its throttle only in the body of a 429", program.Origin.Throttle, want)
	}
	if err := program.Origin.RefuseIncomplete(); err != nil {
		t.Errorf("RefuseIncomplete() = %v, want the GCP reach complete", err)
	}
}

func TestThePreviewEntryProgramReadsEveryReleaseRecordThroughThePreviewTiersAdoptedStore(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adoptIn(environment.TierPreview, fullOffers("c1")); err != nil {
		t.Fatal(err)
	}
	h.keyAssets(t, environment.TierPreview)

	program, err := programming(h).ProgramEdge(t.Context(), entryRequest(environment.TierPreview))
	if err != nil {
		t.Fatalf("ProgramEdge = %v", err)
	}
	if program.ReleasesStore.Name != "ocel-releases-store" || program.ReleasesStore.Endpoint == "" {
		t.Errorf("releases store = %+v, want the store the preview tier adopted", program.ReleasesStore)
	}
}

func TestProgrammingAnEntryWorkerBeforeBootstrapIsNotReady(t *testing.T) {
	t.Parallel()
	_, err := programming(newOffersHarness(t)).ProgramEdge(t.Context(), entryRequest(environment.TierProduction))
	if refusalCode(err) != refusal.CodeNotReady || !strings.Contains(err.Error(), "ocel bootstrap production") {
		t.Errorf("ProgramEdge = %v, want a not-ready refusal naming ocel bootstrap production", err)
	}
}

func TestProgrammingAnEntryWorkerWithoutAnAdoptedClientCertificateIsNotReady(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adopt([]edge.Offer{storeOffer("c1")}); err != nil {
		t.Fatal(err)
	}
	_, err := programming(h).ProgramEdge(t.Context(), entryRequest(environment.TierProduction))
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

func TestAGCPEntryWorkerPresentsItsTiersClientCertificateToTheOrigin(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adopt(fullOffers("c1")); err != nil {
		t.Fatal(err)
	}
	if err := h.adoptIn(environment.TierPreview, fullOffers("c1")); err != nil {
		t.Fatal(err)
	}
	h.keyAssets(t, environment.TierProduction)
	h.keyAssets(t, environment.TierPreview)

	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		program, err := programming(h).ProgramEdge(t.Context(), entryRequest(tier))
		if err != nil {
			t.Fatalf("ProgramEdge(%s) = %v", tier, err)
		}
		if program.Origin.Reach.Kind != edge.ReachClientCertificate || program.Origin.Reach.Certificate == "" {
			t.Errorf("%s reach = %+v, want the tier's client certificate", tier, program.Origin.Reach)
		}
	}
}

func TestTheEntryWorkerReadsAssetsFromTheTiersBucketWithTheKeyOfItsAssetReader(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adopt(fullOffers("c1")); err != nil {
		t.Fatal(err)
	}
	h.keyAssets(t, environment.TierProduction)

	program, err := programming(h).ProgramEdge(t.Context(), entryRequest(environment.TierProduction))
	if err != nil {
		t.Fatalf("ProgramEdge = %v", err)
	}

	want := &edge.OriginStore{
		URL:             "https://storage.googleapis.com/" + h.clients.Bucket(environment.TierProduction),
		Prefix:          "assets/",
		Region:          "auto",
		AccessKeyID:     "GOOG1EREADER",
		SecretAccessKey: "reader-secret",
	}
	if got := program.Origin.Store; got == nil || *got != *want {
		t.Errorf("store = %+v, want %+v: the edge reads assets with the asset reader's key and holds no cached entry the reader cannot list", got, want)
	}
}

func TestProgrammingAnEntryWorkerWithoutAnAssetReaderKeyIsNotReady(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adopt(fullOffers("c1")); err != nil {
		t.Fatal(err)
	}

	_, err := programming(h).ProgramEdge(t.Context(), entryRequest(environment.TierProduction))
	if refusalCode(err) != refusal.CodeNotReady || !strings.Contains(err.Error(), "asset-store") || !strings.Contains(err.Error(), "ocel bootstrap production") {
		t.Errorf("ProgramEdge = %v, want a not-ready refusal naming the asset-store credential and the bootstrap", err)
	}
}
