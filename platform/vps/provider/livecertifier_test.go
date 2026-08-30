package vps_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func placedOnTheBox(t *testing.T, vm machine, at string, names []string, until time.Duration) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(until),
	}
	raw, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	vm.ssh(t, "sudo mkdir -p "+quote(at[:strings.LastIndex(at, "/")]))
	for path, block := range map[string][]byte{
		host.PinCertificate(at): pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}),
		host.PinKey(at):         pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sealed}),
	} {
		vm.ssh(t, "printf %s "+quote(base64.StdEncoding.EncodeToString(block))+
			" | base64 -d | sudo tee "+quote(path)+" >/dev/null")
	}
	vm.ssh(t, "sudo chmod 0600 "+quote(host.PinKey(at)))
}

func TestLiveTheProxyHandleIsReadOffAHandshakeAndAsksTheAdminApiNothing(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	at := host.ProxyPins + "/live"
	placedOnTheBox(t, vm, at, []string{host.ProxyContainer, liveHostname}, 90*24*time.Hour)
	pinned := vm.provider(t, withPins(map[string]string{host.ProxyContainer: at}))
	defer closing(t, pinned)

	_, stack := fronting(t, pinned, "certified")
	one := standsUp(t, pinned, "one")
	promotes(t, stack, "p-one", "one", one, 1)

	ctx := context.Background()
	cert, err := pinned.Certificate(ctx, providerkit.CertificateRequest{
		Kind: boxedge.Kind, Hostname: host.ProxyContainer, Report: edge.DiscardReporter(),
	})
	if err != nil {
		t.Fatalf("Certificate(%s) = %v", host.ProxyContainer, err)
	}
	if cert.ID != certs.PinHandle(at) {
		t.Fatalf("Certificate(%s) = %q, want %q: a hostname a pinned pair covers is served off that pair", host.ProxyContainer, cert.ID, certs.PinHandle(at))
	}
	if cert.Requested {
		t.Error("Certificate().Requested = true on a box, and ocel placed no key material here so it holds authority to remove none")
	}

	served, err := pinned.InspectCertificate(ctx, boxedge.Kind, host.ProxyContainer,
		providerkit.Certificate{ID: certs.ProxyHandle(host.ProxyContainer)})
	if err != nil {
		t.Fatalf("InspectCertificate() over a proxy handle = %v", err)
	}
	if !served.Terminates || !served.Issued || !served.Covers {
		t.Errorf("InspectCertificate() = %+v, want the leaf the proxy served over its own :443 read as issued and covering %s", served, host.ProxyContainer)
	}
	if served.ExpiresAt != 0 {
		t.Errorf("InspectCertificate() reports expiry %d for a certificate the proxy renews, and the number is decorative wherever renewal is healthy", served.ExpiresAt)
	}
	if served.Renewal != certs.ProxyRenewal {
		t.Errorf("InspectCertificate().Renewal = %q, want %q", served.Renewal, certs.ProxyRenewal)
	}

	logs := vm.ssh(t, "sudo docker logs --tail 200 "+host.ProxyContainer+" 2>&1 || true")
	for _, asked := range []string{"/config/apps", "/pki/ca", "admin.api"} {
		if strings.Contains(logs, asked) {
			t.Errorf("reading what the proxy serves reached the admin api (%s): caddy exposes no managed-certificate inventory there, and a 200 from a config load is not evidence a certificate exists because caddy obtains them in the background:\n%s",
				asked, logs)
		}
	}
}

func TestLiveAPinnedPairIsVerifiedFromTheCertificateAndTheKeyIsNeverRead(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	at := host.ProxyPins + "/live-pin"
	placedOnTheBox(t, vm, at, []string{"*.preview.example.invalid"}, 11*24*time.Hour)
	pinned := vm.provider(t, withPins(map[string]string{"*.preview.example.invalid": at}))
	defer closing(t, pinned)

	ctx := context.Background()
	cert, err := pinned.Certificate(ctx, providerkit.CertificateRequest{
		Kind: boxedge.Kind, Hostname: "pr-7.preview.example.invalid", Report: edge.DiscardReporter(),
	})
	if err != nil {
		t.Fatalf("Certificate() over a pinned wildcard = %v", err)
	}
	health, err := pinned.InspectCertificate(ctx, boxedge.Kind, "pr-7.preview.example.invalid", cert)
	if err != nil {
		t.Fatalf("InspectCertificate() = %v", err)
	}
	if !health.Issued || !health.Covers || health.ExpiresAt == 0 || !health.ExpiringSoon {
		t.Errorf("InspectCertificate() = %+v, want the pinned pair read off the box, covering, and warned about: nothing here renews it", health)
	}
	if health.Renewal != certs.PinRenewal {
		t.Errorf("InspectCertificate().Renewal = %q, want %q", health.Renewal, certs.PinRenewal)
	}

	if err := pinned.DiscardCertificate(ctx, cert, edge.DiscardReporter()); err != nil {
		t.Errorf("DiscardCertificate() = %v, want nil", err)
	}
	if !vm.stands(t, host.PinKey(at)) {
		t.Errorf("%s is gone from the box, and ocel never places or removes key material here", host.PinKey(at))
	}
}
