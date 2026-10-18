package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

const (
	cloudflareKind = edge.Kind("cloudflare")
	offeredCAs     = "-----BEGIN CERTIFICATE-----\ncurrent\n-----END CERTIFICATE-----\n"
)

type offersHarness struct {
	secrets *googletest.Secrets
	clients *gcloud.Clients
	records records.Store
	log     *fake.Log
}

func newOffersHarness(t *testing.T) *offersHarness {
	t.Helper()
	h := &offersHarness{secrets: googletest.NewSecrets(), records: fake.NewKeyValues(), log: &fake.Log{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/secrets") {
			h.secrets.Serve(t, w, r)
			return
		}
		t.Errorf("%s %s reached a server that serves only Secret Manager", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	h.clients = pushing(t, server.URL).resolved
	return h
}

func (h *offersHarness) adopt(offers []edge.Offer) error {
	return h.adoptIn(environment.TierProduction, offers)
}

func (h *offersHarness) adoptIn(tier environment.Tier, offers []edge.Offer) error {
	return cloudflareorigin.AdoptOffers(context.Background(), h.clients, h.records, tier, cloudflareKind,
		edge.BootstrapOutput{Offers: offers}, h.log)
}

func (h *offersHarness) record(t *testing.T) (cloudflareorigin.AdoptedEdge, bool) {
	t.Helper()
	entry, err := h.records.Read(context.Background(), cloudflareorigin.AdoptedEdgeKey(environment.TierProduction, cloudflareKind))
	if errors.Is(err, records.ErrNotFound) {
		return cloudflareorigin.AdoptedEdge{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var adopted cloudflareorigin.AdoptedEdge
	if err := json.Unmarshal(entry.Value, &adopted); err != nil {
		t.Fatal(err)
	}
	return adopted, true
}

func (h *offersHarness) credentials(t *testing.T) string {
	t.Helper()
	got, _ := h.secrets.Latest(t, h.clients.EdgeCredentialsSecret(environment.TierProduction, cloudflareKind))
	return got
}

func (h *offersHarness) count(prefix string) int {
	n := 0
	for _, line := range h.log.Lines() {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

func storeOffer(credential string) edge.Offer {
	values := map[string]string{
		edge.OfferKeyStoreEndpoint:   "https://s.example.workers.dev",
		edge.OfferKeyStoreScriptName: "ocel-releases-store",
	}
	if credential != "" {
		values[edge.OfferKeyStoreBootstrapCredential] = credential
	}
	return edge.Offer{Kind: edge.OfferReleasesStore, Values: values}
}

func certificateOffer() edge.Offer {
	return edge.Offer{Kind: edge.OfferWorkerClientCertificate, Values: map[string]string{
		edge.OfferKeyClientCertificateID:          "m1",
		edge.OfferKeyClientCertificateAuthorities: offeredCAs,
	}}
}

func fullOffers(storeCredential string) []edge.Offer {
	return []edge.Offer{storeOffer(storeCredential), certificateOffer()}
}

func TestABootstrapAdoptsTheCloudflareEdgesWorkerAndCertificateAndKeepsItsCredentialInSecretManager(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)

	if err := h.adopt(fullOffers("c1")); err != nil {
		t.Fatalf("adopt = %v", err)
	}

	adopted, found := h.record(t)
	if !found {
		t.Fatal("no record of the adopted edge was written")
	}
	if want := (cloudflareorigin.AdoptedWorker{Endpoint: "https://s.example.workers.dev", ScriptName: "ocel-releases-store"}); adopted.ReleasesStore != want {
		t.Errorf("releases store = %+v, want %+v", adopted.ReleasesStore, want)
	}
	if want := (cloudflareorigin.AdoptedClientCertificate{ID: "m1", Authorities: offeredCAs}); adopted.ClientCertificate != want {
		t.Errorf("client certificate = %+v, want %+v", adopted.ClientCertificate, want)
	}
	if got, want := h.credentials(t), `{"releasesStore":"c1"}`; got != want {
		t.Errorf("credentials secret = %q, want %q", got, want)
	}
	seed, _ := h.secrets.Latest(t, h.clients.PurgeSeedSecret(environment.TierProduction, cloudflareKind))
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(seed) {
		t.Errorf("seed secret = %q, want 64 hex characters", seed)
	}
}

func TestABootstrapKeepsThePurgeSeedItMintedFirst(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adopt(fullOffers("c1")); err != nil {
		t.Fatal(err)
	}
	name := h.clients.PurgeSeedSecret(environment.TierProduction, cloudflareKind)
	first, _ := h.secrets.Latest(t, name)

	if err := h.adopt(fullOffers("c3")); err != nil {
		t.Fatal(err)
	}
	if again, _ := h.secrets.Latest(t, name); again != first {
		t.Errorf("a later bootstrap changed the seed from %q to %q, orphaning every live deployment's purge secret", first, again)
	}
	if got := h.secrets.EnabledVersions(name); len(got) != 1 {
		t.Errorf("the seed has enabled versions %v, want only the first", got)
	}
}

func TestABootstrapReofferedWithoutCredentialsKeepsTheStoredOnes(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adopt(fullOffers("c1")); err != nil {
		t.Fatal(err)
	}
	before, _ := h.record(t)
	added := h.secrets.VersionsAdded()

	if err := h.adopt(fullOffers("")); err != nil {
		t.Fatalf("second adopt = %v", err)
	}
	if got := h.secrets.VersionsAdded(); got != added {
		t.Errorf("a re-offer that changed nothing added %d secret versions", got-added)
	}
	if after, _ := h.record(t); after.ReleasesStore != before.ReleasesStore {
		t.Errorf("record moved from %+v to %+v", before, after)
	}

	if err := h.adopt(fullOffers("c9")); err != nil {
		t.Fatal(err)
	}
	if got, want := h.secrets.VersionsAdded(), added+1; got != want {
		t.Errorf("a new credential added %d versions, want 1", got-added)
	}
	if got, want := h.credentials(t), `{"releasesStore":"c9"}`; got != want {
		t.Errorf("credentials secret = %q, want %q", got, want)
	}
}

func TestABootstrapRefusesAWorkerReofferedWithoutACredentialItNeverStored(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)

	err := h.adopt(fullOffers(""))

	if code := refusalCode(err); code != refusal.CodeInvalid {
		t.Fatalf("adopt = %v, want an invalid refusal", err)
	}
	for _, mentioned := range []string{"ocel-releases-store", h.clients.EdgeCredentialsSecret(environment.TierProduction, cloudflareKind)} {
		if !strings.Contains(err.Error(), mentioned) {
			t.Errorf("refusal %q does not name %q", err, mentioned)
		}
	}
	if _, found := h.record(t); found {
		t.Error("a record was written although an adoption was refused")
	}
}

func TestABootstrapWarnsOfAnOfferNothingAdopts(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)

	offers := append(fullOffers("c1"), edge.Offer{Kind: "mystery"})
	if err := h.adopt(offers); err != nil {
		t.Fatal(err)
	}
	if n := h.count("WARN Ignoring the cloudflare edge's \"mystery\" offer"); n != 1 {
		t.Errorf("%d warnings for the unknown offer, want 1: %v", n, h.log.Lines())
	}
}

func TestAFrontThatOffersNothingLeavesNoRecord(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)

	if err := h.adopt(nil); err != nil {
		t.Fatal(err)
	}
	if _, found := h.record(t); found {
		t.Error("a front that offered nothing left a record")
	}
	if h.secrets.VersionsAdded() != 0 || h.secrets.Has(h.clients.EdgeCredentialsSecret(environment.TierProduction, cloudflareKind)) {
		t.Error("a front that offered nothing left a secret")
	}
}

func TestReadingTheAdoptedEdgeBeforeBootstrapIsNotReady(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	ctx := context.Background()

	_, _, err := cloudflareorigin.RequireAdoptedEdge(ctx, h.clients, h.records, environment.TierProduction, cloudflareKind)
	if refusalCode(err) != refusal.CodeNotReady || !strings.Contains(err.Error(), "ocel bootstrap production") {
		t.Errorf("requireAdoptedEdge = %v, want a not-ready refusal naming ocel bootstrap production", err)
	}
	if _, err = cloudflareorigin.ReadPurgeSeed(ctx, h.clients, environment.TierProduction, cloudflareKind); refusalCode(err) != refusal.CodeNotReady {
		t.Errorf("readPurgeSeed = %v, want a not-ready refusal", err)
	}

	if err := h.adopt(fullOffers("c1")); err != nil {
		t.Fatal(err)
	}
	adopted, creds, err := cloudflareorigin.RequireAdoptedEdge(ctx, h.clients, h.records, environment.TierProduction, cloudflareKind)
	if err != nil || adopted.ReleasesStore.ScriptName != "ocel-releases-store" || creds.ReleasesStore != "c1" {
		t.Errorf("requireAdoptedEdge after bootstrap = %+v %+v %v", adopted, creds, err)
	}
	if seed, err := cloudflareorigin.ReadPurgeSeed(ctx, h.clients, environment.TierProduction, cloudflareKind); err != nil || len(seed) != 64 {
		t.Errorf("readPurgeSeed after bootstrap = %q %v", seed, err)
	}
}

func TestAnAdoptedEdgeRecordSitsUnderTheEdgeStacksBeyondAnyProjectsSlug(t *testing.T) {
	t.Parallel()
	key := cloudflareorigin.AdoptedEdgeKey(environment.TierProduction, cloudflareKind)
	if key.Partition.Root != stackrecords.EdgeStacksPartition(environment.TierProduction).Root || len(key.Path) != 2 {
		t.Errorf("key = %+v, want a two-segment key under the edge stacks", key)
	}
}

func refusalCode(err error) refusal.Code {
	var refused refusal.Refusal
	if errors.As(err, &refused) {
		return refused.Code
	}
	return ""
}
