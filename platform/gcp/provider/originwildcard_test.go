package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const (
	wildcardBase    = "o.example.com"
	shieldedAddress = "34.117.0.8"
	wildcardCert    = "projects/acme-prod/locations/global/certificates/origin"
	workerAuthority = "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"
)

type issuingCertificates struct {
	issued    int
	discarded []provider.Certificate
	failWith  error
}

func (c *issuingCertificates) Issue(ctx context.Context, req provider.CertificateRequest) (provider.Certificate, error) {
	c.issued++
	cert := provider.Certificate{ID: wildcardCert, Requested: true}
	proof := []edge.Record{{Name: "_acme.o.example.com", Type: edge.RecordTypeCNAME, Value: "auth.goog."}}
	cert, err := req.Prove(ctx, cert, proof)
	if err != nil {
		return cert, err
	}
	return cert, c.failWith
}

func (c *issuingCertificates) Inspect(context.Context, edge.Kind, router.Kind, string, provider.Certificate) (provider.CertificateHealth, error) {
	return provider.CertificateHealth{}, nil
}

func (c *issuingCertificates) Discard(_ context.Context, cert provider.Certificate, _ progress.Log) error {
	c.discarded = append(c.discarded, cert)
	return nil
}

type recordingDNS struct {
	written []edge.Record
	foreign map[string]bool
	deleted []edge.Record
}

func (d *recordingDNS) Ensure(_ context.Context, records []edge.Record, say func(string)) ([]edge.Record, error) {
	var wrote []edge.Record
	for _, record := range records {
		if d.foreign[record.Name] {
			say("a record at " + record.Name + " is not ocel's")
			continue
		}
		wrote = append(wrote, record)
		if !slices.Contains(d.written, record) {
			d.written = append(d.written, record)
		}
	}
	return wrote, nil
}

func (d *recordingDNS) Delete(_ context.Context, records []edge.Record) error {
	d.deleted = append(d.deleted, records...)
	return nil
}

func (d *recordingDNS) TTL() time.Duration { return time.Minute }

func (d *recordingDNS) VerifyCredentials(context.Context) error { return nil }

type shieldedBalancer struct {
	reconciled []alb.OriginWildcardSpec
	destroyed  []environment.Tier
}

func (b *shieldedBalancer) ReconcileOriginWildcard(_ context.Context, spec alb.OriginWildcardSpec) (string, error) {
	b.reconciled = append(b.reconciled, spec)
	return shieldedAddress, nil
}

func (b *shieldedBalancer) DestroyOriginWildcard(_ context.Context, tier environment.Tier) error {
	b.destroyed = append(b.destroyed, tier)
	return nil
}

type wildcardHarness struct {
	wildcards    originWildcards
	certificates *issuingCertificates
	dns          *recordingDNS
	balancer     *shieldedBalancer
	records      keyvalue.Store
	adopted      error
}

func newWildcardHarness() *wildcardHarness {
	h := &wildcardHarness{
		certificates: &issuingCertificates{},
		dns:          &recordingDNS{foreign: map[string]bool{}},
		balancer:     &shieldedBalancer{},
		records:      fake.NewKeyValues(),
	}
	h.wildcards = originWildcards{
		keyValues:    h.records,
		certificates: h.certificates,
		openDNS:      func() edge.DNSRecords { return h.dns },
		balancer:     h.balancer,
		readWorkerCAs: func(context.Context, environment.Tier) ([]string, error) {
			if h.adopted != nil {
				return nil, h.adopted
			}
			return []string{workerAuthority}, nil
		},
	}
	return h
}

func (h *wildcardHarness) recorded(t *testing.T) originWildcard {
	t.Helper()
	_, recorded, err := h.wildcards.read(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	return recorded
}

func TestTheOriginWildcardIsCertifiedOnceAndPointedAtTheShieldedAddressWithADNSOnlyRecord(t *testing.T) {
	t.Parallel()
	h := newWildcardHarness()

	if err := h.wildcards.ensure(context.Background(), environment.TierProduction, wildcardBase, nil); err != nil {
		t.Fatalf("ensure = %v", err)
	}

	if h.certificates.issued != 1 {
		t.Errorf("%d certificates issued, want 1", h.certificates.issued)
	}
	pointer := edge.Record{Name: "*." + wildcardBase, Type: edge.RecordTypeA, Value: shieldedAddress, Proxied: false}
	if !slices.Contains(h.dns.written, pointer) {
		t.Errorf("DNS written = %v, want the DNS-only %v", h.dns.written, pointer)
	}
	if !slices.ContainsFunc(h.dns.written, func(r edge.Record) bool { return r.Type == edge.RecordTypeCNAME }) {
		t.Errorf("DNS written = %v, want the certificate's proving record", h.dns.written)
	}
	recorded := h.recorded(t)
	if recorded.BaseDomain != wildcardBase || recorded.Certificate.ID != wildcardCert || recorded.Address != shieldedAddress || len(recorded.Certificate.Written) != 1 {
		t.Errorf("record = %+v, want the base, certificate with its proving record and address", recorded)
	}
	if len(h.balancer.reconciled) != 1 || h.balancer.reconciled[0].Certificate != wildcardCert ||
		!slices.Equal(h.balancer.reconciled[0].ClientCAs, []string{workerAuthority}) {
		t.Errorf("balancer reconciled %+v, want the certificate and the worker's CAs", h.balancer.reconciled)
	}
}

func TestEnsuringTheOriginWildcardTwiceChangesNothing(t *testing.T) {
	t.Parallel()
	h := newWildcardHarness()
	ctx := context.Background()
	if err := h.wildcards.ensure(ctx, environment.TierProduction, wildcardBase, nil); err != nil {
		t.Fatal(err)
	}
	written := len(h.dns.written)

	if err := h.wildcards.ensure(ctx, environment.TierProduction, wildcardBase, nil); err != nil {
		t.Fatal(err)
	}
	if h.certificates.issued != 1 || len(h.dns.written) != written {
		t.Errorf("issued %d times and wrote %d records, want 1 and %d", h.certificates.issued, len(h.dns.written), written)
	}
}

func TestASecondProjectNamingAnotherOriginDomainOnTheSameTierIsRefused(t *testing.T) {
	t.Parallel()
	h := newWildcardHarness()
	ctx := context.Background()
	if err := h.wildcards.ensure(ctx, environment.TierProduction, wildcardBase, nil); err != nil {
		t.Fatal(err)
	}

	err := h.wildcards.ensure(ctx, environment.TierProduction, "x.example.com", nil)
	if refusalCode(err) != refusal.CodeInvalid || !strings.Contains(err.Error(), wildcardBase) {
		t.Errorf("ensure(another base) = %v, want an invalid refusal naming %s", err, wildcardBase)
	}
	if h.certificates.issued != 1 {
		t.Errorf("%d certificates issued, want no second one", h.certificates.issued)
	}
}

func TestAnOriginWildcardStillBeingIssuedIsResumedOnTheNextDeploy(t *testing.T) {
	t.Parallel()
	h := newWildcardHarness()
	ctx := context.Background()
	h.certificates.failWith = provider.Resumable(errors.New("the certificate is still provisioning"))

	err := h.wildcards.ensure(ctx, environment.TierProduction, wildcardBase, nil)
	if _, resumable := provider.ResumableMessage(err); !resumable {
		t.Fatalf("ensure = %v, want a resumable error", err)
	}
	recorded := h.recorded(t)
	if recorded.BaseDomain != wildcardBase || len(recorded.Certificate.Written) != 1 || recorded.Certified {
		t.Errorf("record = %+v, want the base and the proving records kept, uncertified", recorded)
	}

	h.certificates.failWith = nil
	if err := h.wildcards.ensure(ctx, environment.TierProduction, wildcardBase, nil); err != nil {
		t.Fatalf("resumed ensure = %v", err)
	}
	if h.certificates.issued != 2 || !h.recorded(t).Certified {
		t.Errorf("issued %d times, certified %v: the next deploy must ask Google again", h.certificates.issued, h.recorded(t).Certified)
	}
}

func TestAnOriginWildcardWithoutAnAdoptedWorkerCertificateKeepsItsIssuedCertificateAndIsNotReady(t *testing.T) {
	t.Parallel()
	h := newWildcardHarness()
	h.adopted = refusal.Refuse(refusal.CodeNotReady, "run `ocel bootstrap production` again")

	err := h.wildcards.ensure(context.Background(), environment.TierProduction, wildcardBase, nil)
	if refusalCode(err) != refusal.CodeNotReady {
		t.Errorf("ensure = %v, want the not-ready refusal", err)
	}
	if !h.recorded(t).Certificate.Issued() {
		t.Error("the certificate was not recorded before the refusal, and a re-run would issue another")
	}
}

func TestAForeignRecordAtTheOriginWildcardIsRefused(t *testing.T) {
	t.Parallel()
	h := newWildcardHarness()
	h.dns.foreign["*."+wildcardBase] = true

	err := h.wildcards.ensure(context.Background(), environment.TierProduction, wildcardBase, nil)
	if refusalCode(err) != refusal.CodeInvalid || !strings.Contains(err.Error(), "*."+wildcardBase) {
		t.Errorf("ensure = %v, want an invalid refusal naming *.%s", err, wildcardBase)
	}
}

func TestTheOriginWildcardIsNotTakenDownWhileAProjectIsServedBehindTheWorker(t *testing.T) {
	t.Parallel()
	h := newWildcardHarness()
	ctx := context.Background()
	if err := h.wildcards.ensure(ctx, environment.TierProduction, wildcardBase, nil); err != nil {
		t.Fatal(err)
	}
	state := stackrecords.EdgeState{Kind: cloudflare.Kind}
	entry, err := keyvalue.ReadOrEmpty(ctx, h.records, stackrecords.EdgeStackKey(environment.TierProduction, "shop"))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Value, err = json.Marshal(state); err != nil {
		t.Fatal(err)
	}
	if _, err := h.records.Write(ctx, entry); err != nil {
		t.Fatal(err)
	}

	err = h.wildcards.destroy(ctx, environment.TierProduction)
	if refusalCode(err) != refusal.CodeInvalid || !strings.Contains(err.Error(), "shop") {
		t.Errorf("destroy = %v, want an invalid refusal naming shop", err)
	}
	if len(h.balancer.destroyed) != 0 || len(h.certificates.discarded) != 0 {
		t.Error("the refused teardown still took something down")
	}
}

func TestTakingTheOriginWildcardDownReleasesItsCertificateAndRecords(t *testing.T) {
	t.Parallel()
	h := newWildcardHarness()
	ctx := context.Background()
	if err := h.wildcards.ensure(ctx, environment.TierProduction, wildcardBase, nil); err != nil {
		t.Fatal(err)
	}

	if err := h.wildcards.destroy(ctx, environment.TierProduction); err != nil {
		t.Fatalf("destroy = %v", err)
	}
	if !slices.Equal(h.balancer.destroyed, []environment.Tier{environment.TierProduction}) {
		t.Errorf("balancer destroyed %v, want the production entry", h.balancer.destroyed)
	}
	if len(h.certificates.discarded) != 1 || h.certificates.discarded[0].ID != wildcardCert {
		t.Errorf("discarded %v, want the origin certificate", h.certificates.discarded)
	}
	if len(h.dns.deleted) != 2 {
		t.Errorf("deleted %v, want the A record and the proving record", h.dns.deleted)
	}
	if h.recorded(t).BaseDomain != "" {
		t.Error("the record outlived the teardown")
	}
	if err := h.wildcards.destroy(ctx, environment.TierProduction); err != nil {
		t.Errorf("destroying what is gone = %v", err)
	}
}

func TestTheCloudflareFrontThatRunsNoCodeKeepsNoOriginWildcard(t *testing.T) {
	t.Parallel()
	h := newWildcardHarness()
	front := cloudflareFront{
		Edge:      cloudflare.NewProxy("ocel", cloudflare.Options{OriginDomain: wildcardBase}),
		options:   cloudflare.Options{OriginDomain: wildcardBase},
		wildcards: h.wildcards,
	}

	_, _ = front.Reconcile(context.Background(), edge.StackSpec{Slug: "shop", Tier: environment.TierProduction}, edge.StackState{})

	if h.certificates.issued != 0 || len(h.dns.written) != 0 {
		t.Errorf("issued %d certificates and wrote %v for a front that runs no code", h.certificates.issued, h.dns.written)
	}
}

type workerReconcile struct {
	edge.Edge
	certified func() int
	seen      *int
}

func (w workerReconcile) Reconcile(context.Context, edge.StackSpec, edge.StackState) (edge.EdgeStack, error) {
	*w.seen = w.certified()
	return nil, nil
}

func TestTheCloudflareFrontReconcilesTheOriginWildcardBeforeTheWorker(t *testing.T) {
	t.Parallel()
	h := newWildcardHarness()
	relay, err := fake.NewEdges().Open(fake.KindRelay, nil)
	if err != nil {
		t.Fatal(err)
	}
	certified := -1
	front := cloudflareFront{
		Edge:      workerReconcile{Edge: relay, certified: func() int { return h.certificates.issued }, seen: &certified},
		options:   cloudflare.Options{OriginDomain: wildcardBase},
		wildcards: h.wildcards,
	}

	if _, err := front.Reconcile(context.Background(), edge.StackSpec{Slug: "shop", Tier: environment.TierProduction}, edge.StackState{}); err != nil {
		t.Fatalf("Reconcile = %v", err)
	}

	if certified != 1 {
		t.Errorf("the worker was reconciled after %d certificates were issued, want the origin wildcard certified first: its deployments are not reachable before it", certified)
	}
	pruning := edge.StackSpec{Slug: "shop", Tier: environment.TierProduction, PruneOnly: true}
	h2 := newWildcardHarness()
	front.wildcards = h2.wildcards
	if _, err := front.Reconcile(context.Background(), pruning, edge.StackState{}); err != nil || h2.certificates.issued != 0 {
		t.Errorf("a production prune reconcile issued %d certificates (err %v), want none", h2.certificates.issued, err)
	}
}

func TestAPreviewDeployThatOnlyPrunesStillSetsUpThePreviewOriginWildcard(t *testing.T) {
	t.Parallel()
	h := newWildcardHarness()
	relay, err := fake.NewEdges().Open(fake.KindRelay, nil)
	if err != nil {
		t.Fatal(err)
	}
	front := cloudflareFront{
		Edge:      relay,
		options:   cloudflare.Options{OriginDomain: wildcardBase},
		wildcards: h.wildcards,
	}
	pruning := edge.StackSpec{Slug: "shop", Tier: environment.TierPreview, PruneOnly: true}

	if _, err := front.Reconcile(context.Background(), pruning, edge.StackState{}); err != nil {
		t.Fatalf("Reconcile = %v", err)
	}

	if h.certificates.issued != 1 {
		t.Errorf("a preview deploy that hosts on the global preview wildcard issued %d origin certificates, want the preview origin wildcard set up: its serverless apps are refused without it", h.certificates.issued)
	}
}

func TestARenewedWorkerClientCertificateIsTrustedByTheRecordedOriginWildcard(t *testing.T) {
	t.Parallel()
	h := newWildcardHarness()
	if err := h.wildcards.ensure(context.Background(), environment.TierProduction, wildcardBase, nil); err != nil {
		t.Fatal(err)
	}
	renewed := "-----BEGIN CERTIFICATE-----\nBBBB\n-----END CERTIFICATE-----\n"
	offer := edge.Offer{Kind: edge.OfferWorkerClientCertificate, Values: map[string]string{
		edge.OfferKeyClientCertificateAuthorities: renewed + workerAuthority,
	}}

	if err := h.wildcards.trustWorkerClientCertificate(context.Background(), environment.TierProduction, offer); err != nil {
		t.Fatalf("trustWorkerClientCertificate = %v", err)
	}

	last := h.balancer.reconciled[len(h.balancer.reconciled)-1]
	if last.Tier != environment.TierProduction || last.BaseDomain != wildcardBase || last.Certificate != wildcardCert ||
		!slices.Equal(last.ClientCAs, []string{renewed, workerAuthority}) {
		t.Errorf("reconciled %+v, want the recorded wildcard trusting the renewed and the held CA", last)
	}
}

func TestARenewedWorkerClientCertificateOnATierWithNoOriginWildcardTrustsNothing(t *testing.T) {
	t.Parallel()
	h := newWildcardHarness()

	if err := h.wildcards.trustWorkerClientCertificate(context.Background(), environment.TierProduction, edge.Offer{}); err != nil {
		t.Fatalf("trustWorkerClientCertificate = %v", err)
	}
	if len(h.balancer.reconciled) != 0 {
		t.Errorf("reconciled %+v, want nothing: the first deploy that sets the wildcard up reads the adopted CAs", h.balancer.reconciled)
	}
}
