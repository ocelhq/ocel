package caddyfile_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func clientCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{"example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), base64.StdEncoding.EncodeToString(der)
}

var origin = proxy.CertificatePair{
	Certificate: "-----BEGIN CERTIFICATE-----\nORIGIN\n-----END CERTIFICATE-----\n",
	Key:         "-----BEGIN PRIVATE KEY-----\nKEY\n-----END PRIVATE KEY-----\n",
}

func originFile(t *testing.T, front caddyfile.Caddyfile, spec proxy.Spec) proxy.OriginFile {
	t.Helper()
	files, err := front.OriginFiles(spec)
	if err != nil || len(files) != 1 {
		t.Fatalf("OriginFiles() = %+v, %v; want the one origin certificate", files, err)
	}
	return files[0]
}

func TestAShieldedHostnameIsASiteOfItsOwnThatRequiresTheZonesClientCertificateAndServesItsOriginCertificate(t *testing.T) {
	t.Parallel()

	client, trusted := clientCertificate(t)
	successor, trustedNext := clientCertificate(t)
	front := caddyfile.Caddyfile{Container: "caddy", Directory: "/etc/caddy/ocel.d", Network: "web"}
	spec := proxy.Spec{
		Hostnames: []string{"blog.example.com", "shop.example.com", "box.example.com"},
		Shields:   []proxy.Shield{{Hostname: "Shop.Example.com", ClientCertificates: []string{client, successor}, OriginCertificate: origin}},
	}
	rendered, err := front.Render(spec)
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	bundle := originFile(t, front, spec).Path
	want := "blog.example.com, box.example.com {\n" +
		"\treverse_proxy ocel-switchboard:8443 {\n\t\tstream_close_delay 30s\n\t}\n" +
		"}\n" +
		"https://shop.example.com {\n" +
		"\ttls " + bundle + " " + bundle + " {\n" +
		"\t\tclient_auth {\n\t\t\tmode require\n\t\t\ttrusted_leaf_cert " + trusted + "\n\t\t\ttrusted_leaf_cert " + trustedNext + "\n\t\t}\n" +
		"\t}\n" +
		"\treverse_proxy ocel-switchboard:8443 {\n\t\tstream_close_delay 30s\n\t}\n" +
		"}\n" +
		"http://shop.example.com {\n" +
		"\tredir https://{host}{uri} 308\n" +
		"}\n"
	if string(rendered) != want {
		t.Errorf("Render() =\n%s\nwant\n%s", rendered, want)
	}
}

func TestAShieldedHostnameWithNoOriginCertificateRequiresTheClientCertificateOnTheOneYourCaddyOrders(t *testing.T) {
	t.Parallel()

	client, trusted := clientCertificate(t)
	front := caddyfile.Caddyfile{Directory: "/etc/caddy/ocel.d", Port: 8480}
	spec := proxy.Spec{Hostnames: []string{"shop.example.com"}, Shields: []proxy.Shield{{Hostname: "shop.example.com", ClientCertificates: []string{client}}}}
	rendered, err := front.Render(spec)
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	want := "https://shop.example.com {\n" +
		"\ttls {\n\t\tclient_auth {\n\t\t\tmode require\n\t\t\ttrusted_leaf_cert " + trusted + "\n\t\t}\n\t}\n" +
		"\treverse_proxy 127.0.0.1:8480\n" +
		"}\n" +
		"http://shop.example.com {\n\tredir https://{host}{uri} 308\n}\n"
	if string(rendered) != want {
		t.Errorf("Render() =\n%s\nwant\n%s", rendered, want)
	}
	if files, err := front.OriginFiles(spec); err != nil || len(files) != 0 {
		t.Errorf("OriginFiles() = %+v, %v; want none placed for a shield that carries no origin certificate", files, err)
	}
}

func TestAPreviewHostnameIsShieldedByTheWildcardShieldOverItsPreviewBase(t *testing.T) {
	t.Parallel()

	client, _ := clientCertificate(t)
	front := caddyfile.Caddyfile{Directory: "/etc/caddy/ocel.d", Port: 8480}
	spec := proxy.Spec{
		Hostnames: []string{"pr-12--web.preview.example.com", "deep.pr-12--web.preview.example.com"},
		Shields:   []proxy.Shield{{Hostname: "*.preview.example.com", ClientCertificates: []string{client}, OriginCertificate: origin}},
	}
	rendered, err := front.Render(spec)
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	if !strings.Contains(string(rendered), "https://pr-12--web.preview.example.com {\n\ttls "+originFile(t, front, spec).Path) {
		t.Errorf("Render() =\n%s\nwant pr-12--web.preview.example.com served the wildcard's origin certificate", rendered)
	}
	if !strings.HasPrefix(string(rendered), "deep.pr-12--web.preview.example.com {\n") {
		t.Errorf("Render() =\n%s\nwant deep.pr-12--web.preview.example.com left open: a wildcard covers one label", rendered)
	}
}

func TestTheOriginCertificateIsPlacedBesideOcelCaddyAsOneBundleNamedForItsHostnameAndContent(t *testing.T) {
	t.Parallel()

	client, _ := clientCertificate(t)
	front := caddyfile.Caddyfile{Directory: "/etc/caddy/ocel.d", Port: 8480}
	spec := proxy.Spec{
		Hostnames: []string{"pr-12--web.preview.example.com"},
		Shields:   []proxy.Shield{{Hostname: "*.preview.example.com", ClientCertificates: []string{client}, OriginCertificate: origin}},
	}
	placed := originFile(t, front, spec)
	if filepath.Dir(placed.Path) != "/etc/caddy/ocel.d" || !strings.HasPrefix(filepath.Base(placed.Path), switchboard.OriginPrefix+"_.preview.example.com-") ||
		!strings.HasSuffix(placed.Path, ".pem") || strings.HasSuffix(placed.Path, ".caddy") {
		t.Errorf("the origin certificate is placed at %s, want a .pem named for the hostname beside %s, which your Caddy never imports", placed.Path, caddyfile.FileName)
	}
	if string(placed.Bundle) != origin.Certificate+origin.Key {
		t.Errorf("the placed bundle is %q, want the certificate and then its key", placed.Bundle)
	}

	renewed := spec
	renewed.Shields = []proxy.Shield{{Hostname: "*.preview.example.com", ClientCertificates: []string{client},
		OriginCertificate: proxy.CertificatePair{Certificate: "-----BEGIN CERTIFICATE-----\nRENEWED\n-----END CERTIFICATE-----", Key: origin.Key}}}
	next := originFile(t, front, renewed)
	if next.Path == placed.Path {
		t.Errorf("a renewed origin certificate is placed at %s again, want a new name: ocel.caddy then changes and your Caddy reloads onto it", next.Path)
	}
	if !strings.Contains(string(next.Bundle), "-----END CERTIFICATE-----\n-----BEGIN PRIVATE KEY-----") {
		t.Errorf("the placed bundle is %q, want the key on a line of its own after the certificate", next.Bundle)
	}
}

func TestAShieldOverNoHostnameTheBoxServesPlacesNothing(t *testing.T) {
	t.Parallel()

	client, _ := clientCertificate(t)
	front := caddyfile.Caddyfile{Directory: "/etc/caddy/ocel.d", Port: 8480}
	spec := proxy.Spec{Hostnames: []string{"blog.example.com"}, Shields: []proxy.Shield{{Hostname: "shop.example.com", ClientCertificates: []string{client}, OriginCertificate: origin}}}
	if files, err := front.OriginFiles(spec); err != nil || len(files) != 0 {
		t.Errorf("OriginFiles() = %+v, %v; want nothing placed for a hostname ocel.caddy never names", files, err)
	}
}

func TestAClientCertificateThatIsNoCertificateIsRefusedRatherThanRendered(t *testing.T) {
	t.Parallel()

	spec := proxy.Spec{Hostnames: []string{"shop.example.com"}, Shields: []proxy.Shield{{Hostname: "shop.example.com", ClientCertificates: []string{"not a certificate"}}}}
	if _, err := (caddyfile.Caddyfile{Port: 8480}).Render(spec); err == nil {
		t.Error("Render() = nil, want the unreadable client certificate refused: a site that trusts nothing it can read shields nothing")
	}
}

func TestAHostnameAnEdgeForwardsIsAdmittedBehindYourCaddySinceOcelCaddyRendersItsShield(t *testing.T) {
	t.Parallel()

	front := caddyfile.Caddyfile{Box: &box{}, Container: "caddy"}
	if err := front.RefuseUnshielded(context.Background(), "shop.example.com"); err != nil {
		t.Errorf("RefuseUnshielded() = %v, want nil", err)
	}
}

func shieldedBox(t *testing.T) (*box, caddyfile.Caddyfile) {
	t.Helper()
	client, _ := clientCertificate(t)
	machine := &box{
		said:    map[string]string{caddyfile.AdminServers: golden(t, "shielded.json")},
		claimed: []string{"blog.example.com", "shop.example.com", "box.example.com", "pr-1--web.preview.example.com"},
		shields: []proxy.Shield{
			{Hostname: "shop.example.com", ClientCertificates: []string{client}, OriginCertificate: origin},
			{Hostname: "*.preview.example.com", ClientCertificates: []string{client}},
		},
	}
	return machine, caddyfile.Caddyfile{Box: machine, Container: "caddy", Directory: "/d", Network: "web"}
}

func TestTheSitesOcelCaddyRendersForShieldedHostnamesAreOcelsOwnAndNoSiteOfYours(t *testing.T) {
	t.Parallel()

	machine, front := shieldedBox(t)
	if err := front.RefuseRouted(context.Background(), machine.claimed); err != nil {
		t.Errorf("RefuseRouted() = %v, want nothing refused: the open block, each shielded site and the plain-http redirect are all what ocel.caddy renders", err)
	}
}

func TestInspectPassesTheImportAndCollisionsOfAShieldedOcelCaddy(t *testing.T) {
	t.Parallel()

	machine, front := shieldedBox(t)
	rendered, err := front.Render(proxy.Spec{Hostnames: machine.claimed, Shields: machine.shields})
	if err != nil {
		t.Fatal(err)
	}
	machine.placed = summed(rendered)
	checks := checked(t, front)
	for _, subject := range []string{"import of /d/ocel.caddy", "hostnames your Caddy also serves", "/d/ocel.caddy"} {
		if check := checks[subject]; check.Verdict != provider.HostPass {
			t.Errorf("%s = %v: %s, want it to pass", subject, check.Verdict, check.Finding)
		}
	}
}

func TestInspectPassesAShieldedHostnameYourCaddyRefusesToAClientWithNoCertificateAndFailsOneItAnswers(t *testing.T) {
	t.Parallel()

	machine, front := shieldedBox(t)
	machine.answers = map[string]router.Kind{"blog.example.com": switchboard.RouterKind, "box.example.com": switchboard.RouterKind}
	machine.failures = map[string]string{
		"shop.example.com":              "shop.example.com at 127.0.0.1:443: remote error: tls: certificate required",
		"pr-1--web.preview.example.com": "pr-1--web.preview.example.com at 127.0.0.1:443: remote error: tls: certificate required",
	}
	checks := checked(t, front)
	for _, hostname := range []string{"blog.example.com", "shop.example.com", "pr-1--web.preview.example.com"} {
		if check := checks[hostname]; check.Verdict != provider.HostPass {
			t.Errorf("%s = %+v, want it to pass: a shielded hostname is refused to a probe that presents no certificate", hostname, check)
		}
	}

	machine.answers["shop.example.com"] = switchboard.RouterKind
	if check := checked(t, front)["shop.example.com"]; check.Verdict != provider.HostFail || !strings.Contains(check.Finding, "no certificate") {
		t.Errorf("shop.example.com = %+v, want it failed: your Caddy answers it to a client with no certificate", check)
	}
}

func TestASiteOfYoursRedirectingAShieldedHostnameElsewhereStillCollides(t *testing.T) {
	t.Parallel()

	machine, front := shieldedBox(t)
	machine.said[caddyfile.AdminServers] = `{"srv1":{"listen":[":80"],"routes":[{"match":[{"host":["shop.example.com"]}],"handle":[{"handler":"static_response","headers":{"Location":["https://elsewhere.example.net/"]},"status_code":308}]}]}}`
	if err := front.RefuseRouted(context.Background(), []string{"shop.example.com"}); err == nil {
		t.Error("RefuseRouted(shop.example.com) = nil, want it refused: a redirect of yours is no redirect ocel.caddy renders")
	}
}
