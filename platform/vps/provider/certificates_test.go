package vps_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const wildcardPin = "/etc/ocel/preview/certs/wildcard"

func selfSigned(t *testing.T, names []string, until time.Duration) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(until),
	}
	raw, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
}

func pinning(t *testing.T, machine *box, block []byte, at string) *vps.Provider {
	t.Helper()
	if machine.reads == nil {
		machine.reads = map[string]string{}
	}
	machine.reads[host.PinCertificate(at)] = string(block)
	machine.reads[host.PinKey(at)] = "-----BEGIN EC PRIVATE KEY-----\nnothing here may ever reach the cli process\n-----END EC PRIVATE KEY-----\n"
	p := vps.ProviderOver(
		vps.Options{
			SSH:          vps.Target{Host: "box.invalid", User: "ada"},
			Certificates: map[string]string{"*.preview.example.com": at},
		},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	return p
}

func certificateFor(t *testing.T, p *vps.Provider, hostname string) providerkit.Certificate {
	t.Helper()
	cert, err := p.Certificate(context.Background(), providerkit.CertificateRequest{
		Kind:     boxedge.Kind,
		Hostname: hostname,
		Report:   edge.DiscardReporter(),
		Prove: func(context.Context, providerkit.Certificate, []edge.Record) (providerkit.Certificate, error) {
			t.Error("the box asked for a validation record to be proved, and ocel asks no CA on a box")
			return providerkit.Certificate{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Certificate(%s) = %v", hostname, err)
	}
	return cert
}

func TestInspectingAPinnedCertificateNeverOpensTheKeyBesideIt(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p := pinning(t, machine, selfSigned(t, []string{"*.preview.example.com"}, 90*24*time.Hour), wildcardPin)

	cert := certificateFor(t, p, "pr-7.preview.example.com")
	if cert.ID != certs.PinHandle(wildcardPin) {
		t.Fatalf("Certificate() = %q, want %q", cert.ID, certs.PinHandle(wildcardPin))
	}
	if _, err := p.InspectCertificate(context.Background(), boxedge.Kind, "pr-7.preview.example.com", cert); err != nil {
		t.Fatalf("InspectCertificate() = %v", err)
	}

	key := host.PinKey(wildcardPin)
	for _, command := range machine.commands() {
		if strings.Contains(command, key) {
			t.Errorf("the box ran %q, which names %s: ocel reads the certificate to report on it and never the key, and a wildcard private key must not enter the CLI process or the ssh session",
				command, key)
		}
	}
	if machine.at(host.PinCertificate(wildcardPin)) < 0 {
		t.Errorf("nothing read %s, so the expiry reported for a pinned certificate came from somewhere other than the certificate", host.PinCertificate(wildcardPin))
	}
}

func TestAPinnedCertificateReportsItsOwnExpiryAndNamesTheOperatorAsTheRenewer(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p := pinning(t, machine, selfSigned(t, []string{"*.preview.example.com"}, 11*24*time.Hour), wildcardPin)
	cert := certificateFor(t, p, "pr-7.preview.example.com")

	health, err := p.InspectCertificate(context.Background(), boxedge.Kind, "pr-7.preview.example.com", cert)
	if err != nil {
		t.Fatalf("InspectCertificate() = %v", err)
	}
	if !health.Terminates || !health.Issued || !health.Covers {
		t.Errorf("InspectCertificate() = %+v, want a certificate that terminates, is issued and covers the hostname", health)
	}
	if health.ExpiresAt == 0 || !health.ExpiringSoon {
		t.Errorf("InspectCertificate() reports expiry %d and expiring-soon %v, want the parsed NotAfter and a warning: nothing on this box renews a pinned pair",
			health.ExpiresAt, health.ExpiringSoon)
	}
	if health.Renewal != certs.PinRenewal {
		t.Errorf("InspectCertificate().Renewal = %q, want %q", health.Renewal, certs.PinRenewal)
	}
}

func TestAPinThatDoesNotCoverTheHostnameIsRefusedAtBindWithAReasonThatNamesBoth(t *testing.T) {
	t.Parallel()

	machine := &box{reads: map[string]string{
		host.PinCertificate(wildcardPin): string(selfSigned(t, []string{"*.staging.example.com"}, 90*24*time.Hour)),
	}}
	p := vps.ProviderOver(
		vps.Options{
			SSH:          vps.Target{Host: "box.invalid", User: "ada"},
			Certificates: map[string]string{"*.preview.example.com": wildcardPin},
		},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)

	_, err := p.Certificate(context.Background(), providerkit.CertificateRequest{
		Kind: boxedge.Kind, Hostname: "pr-7.preview.example.com", Report: edge.DiscardReporter(),
	})
	var refusal providerkit.Refusal
	if !asRefusal(err, &refusal) {
		t.Fatalf("Certificate() over a pin that covers something else = %v, want a refusal", err)
	}
	if !strings.Contains(refusal.Message, "*.staging.example.com") || !strings.Contains(refusal.Message, "pr-7.preview.example.com") {
		t.Errorf("the refusal reads %q, want it to name what the pinned certificate covers and the hostname it does not", refusal.Message)
	}
}

func TestAnExpiredPinIsRefusedRatherThanServedUnderAHandleThatReadsHealthy(t *testing.T) {
	t.Parallel()

	machine := &box{reads: map[string]string{
		host.PinCertificate(wildcardPin): string(selfSigned(t, []string{"*.preview.example.com"}, -time.Hour)),
	}}
	p := vps.ProviderOver(
		vps.Options{
			SSH:          vps.Target{Host: "box.invalid", User: "ada"},
			Certificates: map[string]string{"*.preview.example.com": wildcardPin},
		},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)

	_, err := p.Certificate(context.Background(), providerkit.CertificateRequest{
		Kind: boxedge.Kind, Hostname: "pr-7.preview.example.com", Report: edge.DiscardReporter(),
	})
	var refusal providerkit.Refusal
	if !asRefusal(err, &refusal) || !strings.Contains(refusal.Message, "expired") {
		t.Fatalf("Certificate() over an expired pin = %v, want a refusal saying so: you placed it and you replace it", err)
	}
}

func TestAnUnpinnedHostnameGetsTheProxysOwnHandleAndAsksNothingOfTheBox(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)

	cert := certificateFor(t, p, "shop.example.com")
	if cert.ID != certs.ProxyHandle("shop.example.com") {
		t.Errorf("Certificate() = %q, want %q", cert.ID, certs.ProxyHandle("shop.example.com"))
	}
	if cert.Requested {
		t.Error("Certificate().Requested = true: Requested is a claim of delete authority, and ocel places no key material on a box so it holds authority to remove none")
	}
	if len(cert.Written) != 0 || len(cert.Owed) != 0 {
		t.Errorf("Certificate() owes records %v/%v, and an http-01 hostname owes no validation record", cert.Written, cert.Owed)
	}
	if len(machine.commands()) != 0 {
		t.Errorf("minting a handle reached the box with %v, and a handle names a slot rather than a certificate that exists", machine.commands())
	}
	if err := p.DiscardCertificate(context.Background(), cert, edge.DiscardReporter()); err != nil {
		t.Errorf("DiscardCertificate() = %v, want nil", err)
	}
}

func TestTheProxyHandleIsReadOffTheHandshakeAndNeverOffCaddysDataDirectory(t *testing.T) {
	t.Parallel()

	served := selfSigned(t, []string{"shop.example.com"}, 60*24*time.Hour)
	machine := &box{leaf: string(served)}
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)

	cert := certificateFor(t, p, "shop.example.com")
	health, err := p.InspectCertificate(context.Background(), boxedge.Kind, "shop.example.com", cert)
	if err != nil {
		t.Fatalf("InspectCertificate() = %v", err)
	}
	if !health.Terminates || !health.Issued || !health.Covers {
		t.Errorf("InspectCertificate() = %+v, want the leaf the proxy served read as issued and covering", health)
	}
	if health.ExpiresAt != 0 || health.ExpiringSoon {
		t.Errorf("InspectCertificate() reports expiry %d, and the number is decorative for a certificate the proxy renews: expiring-soon is false whenever something renewed it",
			health.ExpiresAt)
	}
	if health.Renewal != certs.ProxyRenewal {
		t.Errorf("InspectCertificate().Renewal = %q, want %q", health.Renewal, certs.ProxyRenewal)
	}

	var asked []string
	for _, command := range machine.commands() {
		if strings.Contains(command, "/data") || strings.Contains(command, "caddy-admin.sock") ||
			strings.Contains(command, "/config/") || strings.Contains(command, "upstreams") {
			t.Errorf("reading what the proxy serves ran %q: caddy's admin api exposes no managed-certificate inventory and its data directory is private state a re-pick takes with it", command)
		}
		asked = append(asked, command)
	}
	if machine.at("leaf") < 0 {
		t.Errorf("nothing asked the helper for the leaf, so %v is where the answer came from", asked)
	}
}

func TestAProxyHandleWithNothingServedYetIsPendingRatherThanIssued(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)

	cert := certificateFor(t, p, "shop.example.com")
	health, err := p.InspectCertificate(context.Background(), boxedge.Kind, "shop.example.com", cert)
	if err != nil {
		t.Fatalf("InspectCertificate() over a host the proxy serves nothing for = %v, want it reported rather than refused", err)
	}
	if health.Issued {
		t.Error("InspectCertificate() reports a certificate issued for a host the proxy completed no handshake for; caddy obtains in the background and a 200 from a config load is not evidence one exists")
	}
	if !health.Terminates || health.Renewal == "" {
		t.Errorf("InspectCertificate() = %+v, want it to terminate and to name the renewer even before a certificate exists", health)
	}
}

func asRefusal(err error, refusal *providerkit.Refusal) bool { return errors.As(err, refusal) }
