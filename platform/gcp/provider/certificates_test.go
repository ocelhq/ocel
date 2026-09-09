package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func requested(t *testing.T, server *certServer, hostname string) (providerkit.Certificate, []edge.Record) {
	t.Helper()
	var proved []edge.Record
	cert, err := server.open(t).Certificate(context.Background(), providerkit.CertificateRequest{
		Kind:     alb.Kind,
		Hostname: hostname,
		Report:   edge.DiscardReporter(),
		Prove: func(_ context.Context, cert providerkit.Certificate, records []edge.Record) (providerkit.Certificate, error) {
			proved = records
			cert.Written = records
			return cert, nil
		},
	})
	if err != nil {
		t.Fatalf("Certificate(%s) = %v", hostname, err)
	}
	return cert, proved
}

func TestACertificateIsProvedByTheAuthorizationRecordItsOwnerIsHanded(t *testing.T) {
	t.Parallel()

	server := newCertServer()
	cert, proved := requested(t, server, "shop.example.com")

	if !cert.Held() {
		t.Fatal("Certificate() minted no handle, and a hostname with no certificate is a hostname the front cannot terminate")
	}
	if !cert.Requested {
		t.Error("Certificate().Requested = false, and ocel created this certificate: an unbind must be free to delete it")
	}
	if len(proved) != 1 {
		t.Fatalf("the owner was handed %v to prove, want the one authorization CNAME Certificate Manager asked for", proved)
	}
	if proved[0].Type != edge.RecordTypeCNAME {
		t.Errorf("the owner was asked to write a %q record, want a CNAME: that is what a dns authorization is proved by", proved[0].Type)
	}
	if !strings.Contains(proved[0].Name, "shop.example.com") {
		t.Errorf("the owner was asked to write %q, want a record under the hostname being proved", proved[0].Name)
	}
	if got := server.authorized(); len(got) != 1 {
		t.Errorf("the request left %v standing, want one dns authorization: the certificate is renewed through it", got)
	}
	held := server.certified()[cert.ID]
	if held == nil {
		t.Fatalf("Certificate() reported %q and the project holds %v", cert.ID, server.certified())
	}
	if !slices.Equal(held.Managed.Domains, []string{"shop.example.com"}) {
		t.Errorf("the certificate covers %v, want only the hostname that was asked for", held.Managed.Domains)
	}
	if len(held.Managed.DnsAuthorizations) != 1 {
		t.Errorf("the certificate names %v as its authorizations, want the one that was created for it: without it Certificate Manager never issues",
			held.Managed.DnsAuthorizations)
	}
}

func TestAWildcardIsAuthorizedOnTheDomainUnderneathIt(t *testing.T) {
	t.Parallel()

	server := newCertServer()
	cert, proved := requested(t, server, "*.preview.example.com")

	if strings.Contains(proved[0].Name, "*") {
		t.Errorf("the owner was asked to write %q, and no resolver holds a record under a literal asterisk: a wildcard is proved on the domain beneath it",
			proved[0].Name)
	}
	held := server.certified()[cert.ID]
	if held == nil {
		t.Fatalf("Certificate() reported %q and the project holds %v", cert.ID, server.certified())
	}
	if !slices.Contains(held.Managed.Domains, "*.preview.example.com") {
		t.Errorf("the certificate covers %v, want the wildcard that was asked for", held.Managed.Domains)
	}
}

func TestACertificateManagerRefusedToIssueIsReportedRatherThanWaitedOutForever(t *testing.T) {
	t.Parallel()

	server := newCertServer()
	server.failure = "the authorization record does not resolve"

	var refusal providerkit.Refusal
	_, err := server.open(t).Certificate(context.Background(), providerkit.CertificateRequest{
		Kind:     alb.Kind,
		Hostname: "shop.example.com",
		Report:   edge.DiscardReporter(),
		Prove: func(_ context.Context, cert providerkit.Certificate, _ []edge.Record) (providerkit.Certificate, error) {
			return cert, nil
		},
	})
	if !errors.As(err, &refusal) {
		t.Fatalf("Certificate() against a certificate Google gave up on = %v, want a refusal", err)
	}
	if !strings.Contains(refusal.Message, server.failure) {
		t.Errorf("the refusal reads %q, want Google's own reason in it: the owner is the only one who can fix the record", refusal.Message)
	}
}

func TestACertificateStillProvisioningIsWaitedOutRatherThanReportedIssued(t *testing.T) {
	t.Parallel()

	server := newCertServer()
	server.provisioning = 2
	cert, _ := requested(t, server, "shop.example.com")

	health, err := server.open(t).InspectCertificate(context.Background(), alb.Kind, "shop.example.com", cert)
	if err != nil {
		t.Fatalf("InspectCertificate(%s) = %v", cert.ID, err)
	}
	if !health.Terminates || health.Renewal == "" {
		t.Errorf("InspectCertificate(%s) = %+v, want it to say what terminates the hostname and who renews it", cert.ID, health)
	}
}

func TestAnInspectedCertificateSaysWhatItCoversAndWhenItLapses(t *testing.T) {
	t.Parallel()

	server := newCertServer()
	cert, _ := requested(t, server, "shop.example.com")

	health, err := server.open(t).InspectCertificate(context.Background(), alb.Kind, "shop.example.com", cert)
	if err != nil {
		t.Fatalf("InspectCertificate(%s) = %v", cert.ID, err)
	}
	if !health.Issued {
		t.Errorf("InspectCertificate(%s).Issued = false on an ACTIVE certificate", cert.ID)
	}
	if !health.Covers {
		t.Errorf("InspectCertificate(%s).Covers = false for the hostname it was minted for, and the kit would settle a second one", cert.ID)
	}
	if health.ExpiresAt == 0 {
		t.Errorf("InspectCertificate(%s) names no expiry, and nothing can then warn that a renewal has not happened", cert.ID)
	}
	if health.Status == "" {
		t.Errorf("InspectCertificate(%s) names no status", cert.ID)
	}
}

func TestDiscardingACertificateTakesTheAuthorizationItWasProvedThroughWithIt(t *testing.T) {
	t.Parallel()

	server := newCertServer()
	cert, _ := requested(t, server, "shop.example.com")

	if err := server.open(t).DiscardCertificate(context.Background(), cert, edge.DiscardReporter()); err != nil {
		t.Fatalf("DiscardCertificate(%s) = %v", cert.ID, err)
	}
	dropped := server.dropped()
	if len(dropped) != 2 {
		t.Fatalf("the discard deleted %v, want the certificate and the authorization: bytes a deploy leaves behind after teardown must be zero", dropped)
	}
	if dropped[0] != cert.ID {
		t.Errorf("the discard deleted %q first, want the certificate: an authorization a certificate still names cannot be deleted", dropped[0])
	}
	if !strings.Contains(dropped[1], "dnsAuthorizations/") {
		t.Errorf("the discard deleted %q second, want the dns authorization", dropped[1])
	}
	if got := server.certified(); len(got) != 0 {
		t.Errorf("the project still holds %v after the discard", got)
	}
}

func TestACertificateNothingRequestedIsNotThisProvidersToDelete(t *testing.T) {
	t.Parallel()

	server := newCertServer()
	if err := server.open(t).DiscardCertificate(context.Background(), providerkit.Certificate{}, edge.DiscardReporter()); err != nil {
		t.Errorf("DiscardCertificate() of a binding naming no certificate = %v, want it tolerated", err)
	}
	if got := server.dropped(); len(got) != 0 {
		t.Errorf("the discard deleted %v having been handed no handle", got)
	}
}
