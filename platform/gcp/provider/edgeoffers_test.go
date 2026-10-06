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
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	cloudflareKind = edge.Kind("cloudflare")
	offeredCAs     = "-----BEGIN CERTIFICATE-----\ncurrent\n-----END CERTIFICATE-----\n"
)

type offersHarness struct {
	secrets *secretServer
	clients *clients
	records keyvalue.Store
	log     *fake.Log
}

func newOffersHarness(t *testing.T) *offersHarness {
	t.Helper()
	h := &offersHarness{secrets: newSecretServer(), records: fake.NewKeyValues(), log: &fake.Log{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/secrets") {
			h.secrets.serve(t, w, r)
			return
		}
		t.Errorf("%s %s reached a server that serves only Secret Manager", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	h.clients = pushing(t, server.URL).resolved
	return h
}

func (h *offersHarness) adopt(offers []edge.Offer, values map[string]string) error {
	return h.adoptIn(environment.TierProduction, offers, values)
}

func (h *offersHarness) adoptIn(tier environment.Tier, offers []edge.Offer, values map[string]string) error {
	return adoptEdgeOffers(context.Background(), h.clients, h.records, tier, cloudflareKind,
		edge.BootstrapOutput{Offers: offers, Values: values}, h.log)
}

func (h *offersHarness) record(t *testing.T) (adoptedEdge, bool) {
	t.Helper()
	entry, err := h.records.Read(context.Background(), adoptedEdgeKey(environment.TierProduction, cloudflareKind))
	if errors.Is(err, keyvalue.ErrNotFound) {
		return adoptedEdge{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var adopted adoptedEdge
	if err := json.Unmarshal(entry.Value, &adopted); err != nil {
		t.Fatal(err)
	}
	return adopted, true
}

func (h *offersHarness) credentials(t *testing.T) string {
	t.Helper()
	got, _ := h.secrets.latest(t, h.clients.EdgeCredentialsSecret(environment.TierProduction, cloudflareKind))
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
		edge.OfferKeyStoreScriptName: "ocel-deployments-store",
	}
	if credential != "" {
		values[edge.OfferKeyStoreBootstrapCredential] = credential
	}
	return edge.Offer{Kind: edge.OfferDeploymentsStore, Values: values}
}

func writerOffer(credential string) edge.Offer {
	values := map[string]string{
		edge.OfferKeyISRWriterEndpoint:   "https://w.example.workers.dev",
		edge.OfferKeyISRWriterScriptName: "ocel-isr-writer",
	}
	if credential != "" {
		values[edge.OfferKeyISRWriterBootstrapCredential] = credential
	}
	return edge.Offer{Kind: edge.OfferISRWriter, Values: values}
}

func certificateOffer() edge.Offer {
	return edge.Offer{Kind: edge.OfferWorkerClientCertificate, Values: map[string]string{
		edge.OfferKeyClientCertificateID:          "m1",
		edge.OfferKeyClientCertificateAuthorities: offeredCAs,
	}}
}

func cacheOffer() edge.Offer {
	return edge.Offer{Kind: edge.OfferCacheStore, Values: map[string]string{edge.OfferKeyBucket: "ocel-edge-cache", edge.OfferKeySecretAccessKey: "r2-secret"}}
}

func fullOffers(storeCredential, writerCredential string) []edge.Offer {
	return []edge.Offer{storeOffer(storeCredential), writerOffer(writerCredential), certificateOffer(), cacheOffer()}
}

func TestABootstrapAdoptsTheCloudflareEdgesWorkersAndCertificateAndKeepsTheirCredentialsInSecretManager(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)

	if err := h.adopt(fullOffers("c1", "c2"), map[string]string{"cacheBucket": "ocel-edge-cache"}); err != nil {
		t.Fatalf("adopt = %v", err)
	}

	adopted, found := h.record(t)
	if !found {
		t.Fatal("no record of the adopted edge was written")
	}
	if want := (adoptedWorker{Endpoint: "https://s.example.workers.dev", ScriptName: "ocel-deployments-store"}); adopted.DeploymentsStore != want {
		t.Errorf("deployments store = %+v, want %+v", adopted.DeploymentsStore, want)
	}
	if want := (adoptedWorker{Endpoint: "https://w.example.workers.dev", ScriptName: "ocel-isr-writer"}); adopted.ISRWriter != want {
		t.Errorf("isr writer = %+v, want %+v", adopted.ISRWriter, want)
	}
	if want := (adoptedClientCertificate{ID: "m1", Authorities: offeredCAs}); adopted.ClientCertificate != want {
		t.Errorf("client certificate = %+v, want %+v", adopted.ClientCertificate, want)
	}
	if adopted.Values["cacheBucket"] != "ocel-edge-cache" {
		t.Errorf("values = %v, want the bootstrap's cacheBucket", adopted.Values)
	}
	if got, want := h.credentials(t), `{"deploymentsStore":"c1","isrWriter":"c2"}`; got != want {
		t.Errorf("credentials secret = %q, want %q", got, want)
	}
	seed, _ := h.secrets.latest(t, h.clients.ISRWriterSeedSecret(environment.TierProduction, cloudflareKind))
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(seed) {
		t.Errorf("seed secret = %q, want 64 hex characters", seed)
	}
}

func TestABootstrapKeepsTheISRWriterSeedItMintedFirst(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adopt(fullOffers("c1", "c2"), nil); err != nil {
		t.Fatal(err)
	}
	name := h.clients.ISRWriterSeedSecret(environment.TierProduction, cloudflareKind)
	first, _ := h.secrets.latest(t, name)

	if err := h.adopt(fullOffers("c3", "c4"), nil); err != nil {
		t.Fatal(err)
	}
	if again, _ := h.secrets.latest(t, name); again != first {
		t.Errorf("a later bootstrap changed the seed from %q to %q, orphaning every live deployment's write secret", first, again)
	}
	if got := h.secrets.enabledVersions(name); len(got) != 1 {
		t.Errorf("the seed has enabled versions %v, want only the first", got)
	}
}

func TestABootstrapReofferedWithoutCredentialsKeepsTheStoredOnes(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adopt(fullOffers("c1", "c2"), map[string]string{"cacheBucket": "b"}); err != nil {
		t.Fatal(err)
	}
	before, _ := h.record(t)
	added := h.secrets.versionsAdded()

	if err := h.adopt(fullOffers("", ""), map[string]string{"cacheBucket": "b"}); err != nil {
		t.Fatalf("second adopt = %v", err)
	}
	if got := h.secrets.versionsAdded(); got != added {
		t.Errorf("a re-offer that changed nothing added %d secret versions", got-added)
	}
	if after, _ := h.record(t); after.DeploymentsStore != before.DeploymentsStore || after.ISRWriter != before.ISRWriter {
		t.Errorf("record moved from %+v to %+v", before, after)
	}

	if err := h.adopt(fullOffers("c9", ""), map[string]string{"cacheBucket": "b"}); err != nil {
		t.Fatal(err)
	}
	if got, want := h.secrets.versionsAdded(), added+1; got != want {
		t.Errorf("a new credential for one worker added %d versions, want 1", got-added)
	}
	if got, want := h.credentials(t), `{"deploymentsStore":"c9","isrWriter":"c2"}`; got != want {
		t.Errorf("credentials secret = %q, want %q: the other worker's credential is kept", got, want)
	}
}

func TestABootstrapRefusesAWorkerReofferedWithoutACredentialItNeverStored(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)

	err := h.adopt(fullOffers("c1", ""), nil)

	if code := refusalCode(err); code != refusal.CodeInvalid {
		t.Fatalf("adopt = %v, want an invalid refusal", err)
	}
	for _, mentioned := range []string{"ocel-isr-writer", h.clients.EdgeCredentialsSecret(environment.TierProduction, cloudflareKind)} {
		if !strings.Contains(err.Error(), mentioned) {
			t.Errorf("refusal %q does not name %q", err, mentioned)
		}
	}
	if _, found := h.record(t); found {
		t.Error("a record was written although an adoption was refused")
	}
}

func TestABootstrapLeavesTheEdgesCacheStoreToItsWorkers(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)

	if err := h.adopt(fullOffers("c1", "c2"), nil); err != nil {
		t.Fatal(err)
	}
	if n := h.count("WARN"); n != 0 {
		t.Errorf("%d warnings, want none: the cache store is on every bootstrap by design", n)
	}
	if n := h.count("INFO Leaving the cloudflare edge's cache store"); n != 1 {
		t.Errorf("%d notices about the cache store, want 1: %v", n, h.log.Lines())
	}
	adopted, _ := h.record(t)
	raw, _ := json.Marshal(adopted)
	if strings.Contains(string(raw), "r2-secret") || strings.Contains(h.credentials(t), "r2-secret") {
		t.Errorf("the cache store's key pair was stored: %s", raw)
	}
}

func TestABootstrapWarnsOfAnOfferNothingAdopts(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)

	offers := append(fullOffers("c1", "c2"), edge.Offer{Kind: "mystery"})
	if err := h.adopt(offers, nil); err != nil {
		t.Fatal(err)
	}
	if n := h.count("WARN Ignoring the cloudflare edge's \"mystery\" offer"); n != 1 {
		t.Errorf("%d warnings for the unknown offer, want 1: %v", n, h.log.Lines())
	}
}

func TestAFrontThatOffersNothingLeavesNoRecord(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)

	if err := h.adopt(nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, found := h.record(t); found {
		t.Error("a front that offered nothing left a record")
	}
	if h.secrets.versionsAdded() != 0 || h.secrets.has(h.clients.EdgeCredentialsSecret(environment.TierProduction, cloudflareKind)) {
		t.Error("a front that offered nothing left a secret")
	}
}

func TestReadingTheAdoptedEdgeBeforeBootstrapIsNotReady(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	ctx := context.Background()

	_, _, err := readAdoptedEdge(ctx, h.clients, h.records, environment.TierProduction, cloudflareKind)
	if refusalCode(err) != refusal.CodeNotReady || !strings.Contains(err.Error(), "ocel bootstrap production") {
		t.Errorf("readAdoptedEdge = %v, want a not-ready refusal naming ocel bootstrap production", err)
	}
	if _, err = readISRWriterSeed(ctx, h.clients, environment.TierProduction, cloudflareKind); refusalCode(err) != refusal.CodeNotReady {
		t.Errorf("readISRWriterSeed = %v, want a not-ready refusal", err)
	}

	if err := h.adopt(fullOffers("c1", "c2"), nil); err != nil {
		t.Fatal(err)
	}
	adopted, creds, err := readAdoptedEdge(ctx, h.clients, h.records, environment.TierProduction, cloudflareKind)
	if err != nil || adopted.ISRWriter.ScriptName != "ocel-isr-writer" || creds.DeploymentsStore != "c1" || creds.ISRWriter != "c2" {
		t.Errorf("readAdoptedEdge after bootstrap = %+v %+v %v", adopted, creds, err)
	}
	if seed, err := readISRWriterSeed(ctx, h.clients, environment.TierProduction, cloudflareKind); err != nil || len(seed) != 64 {
		t.Errorf("readISRWriterSeed after bootstrap = %q %v", seed, err)
	}
}

func TestAnAdoptedEdgeRecordSitsUnderTheEdgeStacksBeyondAnyProjectsSlug(t *testing.T) {
	t.Parallel()
	key := adoptedEdgeKey(environment.TierProduction, cloudflareKind)
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
