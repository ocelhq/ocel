package gcp

import (
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/originsecret"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/control"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
)

func programming(h *offersHarness) *Provider {
	return newProvider(&control.Provider{Namespace: "ocel", Clients: h.clients, Records: h.records})
}

func (h *offersHarness) keyAssets(t *testing.T, tier environment.Tier) {
	t.Helper()
	credentials, err := cloudflareorigin.ReadEdgeCredentials(t.Context(), h.clients, tier, cloudflareKind)
	if err != nil {
		t.Fatal(err)
	}
	credentials.AssetStoreAccessKeyID, credentials.AssetStoreSecretAccessKey = "GOOG1EREADER", "reader-secret"
	if err := cloudflareorigin.WriteEdgeCredentials(t.Context(), h.clients, tier, cloudflareKind, credentials); err != nil {
		t.Fatal(err)
	}
}

func (h *offersHarness) bootstrapForProgramming(t *testing.T, tier environment.Tier) originsecret.Secret {
	t.Helper()
	if err := h.adoptIn(tier, fullOffers("c1")); err != nil {
		t.Fatal(err)
	}
	h.keyAssets(t, tier)
	if err := h.ensureOriginSecret(t, tier, time.Now()); err != nil {
		t.Fatal(err)
	}
	return h.storedOriginSecret(t, tier)
}

func entryRequest(tier environment.Tier) provider.EdgeProgramRequest {
	return provider.EdgeProgramRequest{Tier: tier, Kind: cloudflareKind}
}

func TestTheEntryWorkerProgramReachesGCPByPresentingTheTiersOriginSecretAndHoldsNoAWSKeys(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	secret := h.bootstrapForProgramming(t, environment.TierProduction)

	program, err := programming(h).ProgramEdge(t.Context(), entryRequest(environment.TierProduction))
	if err != nil {
		t.Fatalf("ProgramEdge = %v", err)
	}

	wantStore := edge.ReleasesStore{Name: "ocel-releases-store", Endpoint: "https://s.example.workers.dev", BootstrapCredential: "c1"}
	if program.ReleasesStore != wantStore {
		t.Errorf("releases store = %+v, want %+v", program.ReleasesStore, wantStore)
	}
	if want := (edge.Reach{Kind: edge.ReachOriginSecret, Secret: secret.Current}); program.Origin.Reach != want {
		t.Errorf("reach = %+v, want %+v: the worker reaches each revision-tag URL by presenting the tier's origin secret, with no certificate or signing keys", program.Origin.Reach, want)
	}
	if want := (edge.Throttle{Status: 429, BodyContains: "no available instance"}); program.Origin.Throttle != want {
		t.Errorf("throttle = %+v, want %+v: Cloud Run names its throttle only in the body of a 429", program.Origin.Throttle, want)
	}
	if err := program.Origin.RefuseIncomplete(); err != nil {
		t.Errorf("RefuseIncomplete() = %v, want the GCP reach complete", err)
	}
}

func TestAnEntryWorkerProgrammedDuringARotationPresentsTheReplacedSecretToReleasesMadeBeforeIt(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adopt(fullOffers("c1")); err != nil {
		t.Fatal(err)
	}
	h.keyAssets(t, environment.TierProduction)
	rotatedAt := originSecretMintedAt.Add(originsecret.MaxAge)
	h.recordOriginSecretAs(t, originsecret.Secret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt})

	program, err := programming(h).ProgramEdge(t.Context(), entryRequest(environment.TierProduction))
	if err != nil {
		t.Fatalf("ProgramEdge = %v", err)
	}

	want := edge.Reach{Kind: edge.ReachOriginSecret, Secret: "s2", PreviousSecret: "s1", PreviousFingerprint: edge.OriginSecretFingerprint("s1")}
	if program.Origin.Reach != want {
		t.Errorf("reach = %+v, want %+v: a release made before the rotation accepts only the secret it replaced", program.Origin.Reach, want)
	}
	if err := program.Origin.RefuseIncomplete(); err != nil {
		t.Errorf("RefuseIncomplete() = %v, want the rotating reach complete", err)
	}
}

func TestThePreviewEntryProgramReadsEveryReleaseRecordThroughThePreviewTiersAdoptedStore(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	h.bootstrapForProgramming(t, environment.TierPreview)

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

func TestProgrammingAnEntryWorkerOnATierWithNoOriginSecretIsNotReady(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adopt(fullOffers("c1")); err != nil {
		t.Fatal(err)
	}
	h.keyAssets(t, environment.TierProduction)

	_, err := programming(h).ProgramEdge(t.Context(), entryRequest(environment.TierProduction))
	if refusalCode(err) != refusal.CodeNotReady || !strings.Contains(err.Error(), "origin secret") || !strings.Contains(err.Error(), "ocel bootstrap production") {
		t.Errorf("ProgramEdge = %v, want a not-ready refusal naming the origin secret and ocel bootstrap production", err)
	}
}

func TestTheProviderHandsTheEntryWorkerProgramToItsHooks(t *testing.T) {
	t.Parallel()
	if newProvider(&control.Provider{}).Hooks().ProgramEdge == nil {
		t.Error("Hooks().ProgramEdge is unset, so no edge that runs code gets a program")
	}
}

func TestAGCPEntryWorkerPresentsItsOwnTiersOriginSecret(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	secrets := map[environment.Tier]originsecret.Secret{}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		secrets[tier] = h.bootstrapForProgramming(t, tier)
	}

	for tier, secret := range secrets {
		program, err := programming(h).ProgramEdge(t.Context(), entryRequest(tier))
		if err != nil {
			t.Fatalf("ProgramEdge(%s) = %v", tier, err)
		}
		if program.Origin.Reach.Kind != edge.ReachOriginSecret || program.Origin.Reach.Secret != secret.Current {
			t.Errorf("%s reach = %+v, want the tier's own origin secret", tier, program.Origin.Reach)
		}
	}
}

func TestTheEntryWorkerReadsAssetsFromTheTiersBucketWithTheKeyOfItsAssetReader(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	h.bootstrapForProgramming(t, environment.TierProduction)

	program, err := programming(h).ProgramEdge(t.Context(), entryRequest(environment.TierProduction))
	if err != nil {
		t.Fatalf("ProgramEdge = %v", err)
	}

	want := &edge.OriginStore{
		URL:             "https://storage.googleapis.com/ocel-prod-edge-k3x9q2",
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
