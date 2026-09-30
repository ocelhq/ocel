package traefik_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func clientCertificate(t *testing.T) string {
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
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

var origin = proxy.CertificatePair{
	Certificate: "-----BEGIN CERTIFICATE-----\nORIGIN\n-----END CERTIFICATE-----\n",
	Key:         "-----BEGIN PRIVATE KEY-----\nKEY\n-----END PRIVATE KEY-----\n",
}

func coolifyTraefik() traefik.Traefik {
	return traefik.Traefik{
		Directory:          "/data/coolify/proxy/dynamic",
		ContainerDirectory: "/traefik/dynamic",
		Resolver:           "letsencrypt",
		HTTP:               "http",
		HTTPS:              "https",
		Network:            "coolify",
	}
}

type readRouter struct {
	Rule        string   `yaml:"rule"`
	EntryPoints []string `yaml:"entryPoints"`
	Middlewares []string `yaml:"middlewares"`
	Service     string   `yaml:"service"`
	TLS         *struct {
		CertResolver string `yaml:"certResolver"`
		Options      string `yaml:"options"`
	} `yaml:"tls"`
}

type readOptions struct {
	SniStrict  bool `yaml:"sniStrict"`
	ClientAuth struct {
		CAFiles        []string `yaml:"caFiles"`
		ClientAuthType string   `yaml:"clientAuthType"`
	} `yaml:"clientAuth"`
}

type readConfig struct {
	HTTP struct {
		Routers     map[string]readRouter `yaml:"routers"`
		Middlewares map[string]struct {
			Retry *struct {
				Attempts        int    `yaml:"attempts"`
				InitialInterval string `yaml:"initialInterval"`
			} `yaml:"retry"`
		} `yaml:"middlewares"`
		Services map[string]struct {
			LoadBalancer struct {
				Servers []struct {
					URL string `yaml:"url"`
				} `yaml:"servers"`
			} `yaml:"loadBalancer"`
		} `yaml:"services"`
	} `yaml:"http"`
	TLS struct {
		Certificates []struct {
			CertFile string `yaml:"certFile"`
			KeyFile  string `yaml:"keyFile"`
		} `yaml:"certificates"`
		Options map[string]readOptions `yaml:"options"`
	} `yaml:"tls"`
}

func rendered(t *testing.T, front traefik.Traefik, spec proxy.Spec) readConfig {
	t.Helper()
	written, err := front.Render(spec)
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	if err := front.Validate(context.Background(), written); err != nil {
		t.Fatalf("Validate() = %v on what Render wrote:\n%s", err, written)
	}
	var read readConfig
	if err := yaml.Unmarshal(written, &read); err != nil {
		t.Fatal(err)
	}
	return read
}

func routerFor(t *testing.T, read readConfig, hostname, entryPoint string) readRouter {
	t.Helper()
	for _, each := range read.HTTP.Routers {
		if each.Rule == "Host(`"+hostname+"`)" && len(each.EntryPoints) == 1 && each.EntryPoints[0] == entryPoint {
			return each
		}
	}
	t.Fatalf("no router on %s for %s in %+v", entryPoint, hostname, read.HTTP.Routers)
	return readRouter{}
}

func TestAShieldedHostnameRequiresTheZonesClientCertificatesAndServesItsOriginCertificate(t *testing.T) {
	t.Parallel()

	client, successor := clientCertificate(t), clientCertificate(t)
	front := coolifyTraefik()
	spec := proxy.Spec{
		Hostnames: []string{"blog.example.com", "shop.example.com"},
		Shields:   []proxy.Shield{{Hostname: "Shop.Example.com", ClientCAs: []string{client, successor}, OriginCertificate: origin}},
	}
	read := rendered(t, front, spec)

	if open := routerFor(t, read, "blog.example.com", "https"); open.TLS == nil || open.TLS.CertResolver != "letsencrypt" || open.TLS.Options != "" {
		t.Errorf("the unshielded router is %+v, want its certificate ordered through letsencrypt and no client certificate required", open)
	}
	shielded := routerFor(t, read, "shop.example.com", "https")
	if shielded.TLS == nil || shielded.TLS.CertResolver != "" || shielded.TLS.Options == "" {
		t.Fatalf("the shielded router is %+v, want TLS options of its own and no certificate ordered: it answers with its origin certificate", shielded)
	}
	options, found := read.TLS.Options[shielded.TLS.Options]
	if !found {
		t.Fatalf("the shielded router names TLS options %q, which ocel.yml does not define: %+v", shielded.TLS.Options, read.TLS.Options)
	}
	if options.ClientAuth.ClientAuthType != "RequireAndVerifyClientCert" || !options.SniStrict {
		t.Errorf("the shield's TLS options are %+v, want a verified client certificate required and SNI checked strictly", options)
	}
	if strings.Join(options.ClientAuth.CAFiles, "") != client+successor && strings.Join(options.ClientAuth.CAFiles, "") != successor+client {
		t.Errorf("the shield trusts %q, want both client certificates the zone holds, inline", options.ClientAuth.CAFiles)
	}

	bundle := originFile(t, front, spec)
	if !strings.HasPrefix(bundle.Path, front.Directory+"/"+switchboard.OriginPrefix) {
		t.Errorf("the origin certificate is placed at %s, want it in %s, where the switchboard places files", bundle.Path, front.Directory)
	}
	readAs := "/traefik/dynamic/" + bundle.Path[len(front.Directory)+1:]
	if len(read.TLS.Certificates) != 1 || read.TLS.Certificates[0].CertFile != readAs || read.TLS.Certificates[0].KeyFile != readAs {
		t.Errorf("ocel.yml serves certificates %+v, want the bundle named where your Traefik reads it, %s", read.TLS.Certificates, readAs)
	}
}

func TestAShieldedHostnameIsRedirectedFromHTTPAndForwardedNowhere(t *testing.T) {
	t.Parallel()

	spec := proxy.Spec{
		Hostnames: []string{"shop.example.com"},
		Shields:   []proxy.Shield{{Hostname: "shop.example.com", ClientCAs: []string{clientCertificate(t)}, OriginCertificate: origin}},
	}
	plain := routerFor(t, rendered(t, coolifyTraefik(), spec), "shop.example.com", "http")
	if plain.Service != "noop@internal" || len(plain.Middlewares) != 1 {
		t.Errorf("the http router is %+v, want it answered by the redirect alone, forwarding to nothing", plain)
	}
}

func TestAShieldedHostnameWithNoOriginCertificateRequiresTheClientCertificateOnTheOneYourTraefikOrders(t *testing.T) {
	t.Parallel()

	spec := proxy.Spec{
		Hostnames: []string{"shop.example.com"},
		Shields:   []proxy.Shield{{Hostname: "shop.example.com", ClientCAs: []string{clientCertificate(t)}}},
	}
	read := rendered(t, coolifyTraefik(), spec)
	shielded := routerFor(t, read, "shop.example.com", "https")
	if shielded.TLS == nil || shielded.TLS.CertResolver != "letsencrypt" || shielded.TLS.Options == "" {
		t.Errorf("the shielded router is %+v, want its certificate ordered through letsencrypt and a client certificate required", shielded)
	}
	if len(read.TLS.Certificates) != 0 {
		t.Errorf("ocel.yml serves certificates %+v, want none: the hostname has no origin certificate", read.TLS.Certificates)
	}
}

func TestAPreviewHostnameIsShieldedByTheWildcardShieldOverItsPreviewBase(t *testing.T) {
	t.Parallel()

	spec := proxy.Spec{
		Hostnames:   []string{"pr-1-web-abcdefghijklmnopfhzq6k4d.preview.example.com", "shop.example.com"},
		PreviewBase: "preview.example.com",
		Shields:     []proxy.Shield{{Hostname: "*.preview.example.com", ClientCAs: []string{clientCertificate(t)}, OriginCertificate: origin}},
	}
	read := rendered(t, coolifyTraefik(), spec)
	if previewed := routerFor(t, read, "pr-1-web-abcdefghijklmnopfhzq6k4d.preview.example.com", "https"); previewed.TLS == nil || previewed.TLS.Options == "" {
		t.Errorf("the preview router is %+v, want the wildcard's shield on it", previewed)
	}
	if open := routerFor(t, read, "shop.example.com", "https"); open.TLS == nil || open.TLS.Options != "" {
		t.Errorf("the router of a hostname no shield covers is %+v, want no client certificate required", open)
	}
}

func TestWithoutAContainerDirectoryTheOriginCertificateIsNamedWhereItIsPlaced(t *testing.T) {
	t.Parallel()

	front := traefik.Traefik{Directory: "/etc/traefik/dynamic", Resolver: "letsencrypt", HTTP: "web", HTTPS: "websecure", Port: 8480}
	spec := proxy.Spec{
		Hostnames: []string{"shop.example.com"},
		Shields:   []proxy.Shield{{Hostname: "shop.example.com", ClientCAs: []string{clientCertificate(t)}, OriginCertificate: origin}},
	}
	read := rendered(t, front, spec)
	if bundle := originFile(t, front, spec); len(read.TLS.Certificates) != 1 || read.TLS.Certificates[0].CertFile != bundle.Path {
		t.Errorf("ocel.yml serves certificates %+v, want the bundle at %s", read.TLS.Certificates, bundle.Path)
	}
}

func originFile(t *testing.T, front traefik.Traefik, spec proxy.Spec) proxy.OriginFile {
	t.Helper()
	files, err := front.OriginFiles(spec)
	if err != nil || len(files) != 1 {
		t.Fatalf("OriginFiles() = %+v, %v; want the one origin certificate", files, err)
	}
	if want := origin.Certificate + origin.Key; string(files[0].Bundle) != want {
		t.Errorf("the bundle holds %q, want the origin certificate then its key", files[0].Bundle)
	}
	return files[0]
}

func TestAClientCertificateThatIsNoCertificateIsRefusedRatherThanRendered(t *testing.T) {
	t.Parallel()

	spec := proxy.Spec{
		Hostnames: []string{"shop.example.com"},
		Shields:   []proxy.Shield{{Hostname: "shop.example.com", ClientCAs: []string{"not a certificate"}}},
	}
	if _, err := coolifyTraefik().Render(spec); err == nil {
		t.Error("Render() = nil, want the client certificate refused: your Traefik would trust nothing it names and refuse every request")
	}
}

func TestAHostnameAnEdgeForwardsIsAdmittedBehindYourTraefikSinceOcelYmlRendersItsShield(t *testing.T) {
	t.Parallel()

	if err := coolifyTraefik().RefuseUnshielded(context.Background(), "shop.example.com"); err != nil {
		t.Errorf("RefuseUnshielded() = %v, want nil: ocel.yml requires the edge's client certificate of a shielded hostname", err)
	}
}

func TestInspectPassesAShieldedHostnameYourTraefikRefusesToAClientWithNoCertificateAndFailsOneItAnswers(t *testing.T) {
	t.Parallel()

	front := coolifyTraefik()
	spec := proxy.Spec{
		Hostnames: []string{"shop.example.com", "blog.example.com"},
		Shields:   []proxy.Shield{{Hostname: "shop.example.com", ClientCAs: []string{clientCertificate(t)}, OriginCertificate: origin}},
	}
	for _, tc := range []struct {
		name    string
		answers []string
		verdict provider.HostVerdict
	}{
		{name: "refused", verdict: provider.HostPass},
		{name: "answered", answers: []string{string(switchboard.RouterKind)}, verdict: provider.HostFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &box{spec: spec, board: "mount=/data/coolify/proxy/dynamic network=coolify",
				routes:   map[string][]string{"shop.example.com": tc.answers, "blog.example.com": {string(switchboard.RouterKind)}},
				failures: map[string]string{"shop.example.com": "remote error: tls: certificate required"},
				leaves:   map[string][]byte{"blog.example.com": certificate(t, "blog.example.com")}}
			front.Box = b
			checks, err := front.Inspect(context.Background())
			if err != nil {
				t.Fatalf("Inspect() = %v", err)
			}
			var shop []provider.HostCheck
			for _, check := range checks {
				if strings.HasPrefix(check.Subject, "shop.example.com") {
					shop = append(shop, check)
				}
			}
			if len(shop) != 1 || shop[0].Verdict != tc.verdict {
				t.Errorf("Inspect() checks shop.example.com with %+v, want one check with verdict %v", shop, tc.verdict)
			}
		})
	}
}

func TestReloadTakesAShieldedHostnameAsRoutedOnceYourTraefikRefusesItToAClientWithNoCertificate(t *testing.T) {
	t.Parallel()

	front := coolifyTraefik()
	spec := proxy.Spec{
		Hostnames: []string{"shop.example.com"},
		Shields:   []proxy.Shield{{Hostname: "shop.example.com", ClientCAs: []string{clientCertificate(t)}, OriginCertificate: origin}},
	}
	b := &box{spec: spec,
		routes:   map[string][]string{"shop.example.com": {string(switchboard.RouterKind), string(switchboard.RouterKind), ""}},
		failures: map[string]string{"shop.example.com": "remote error: tls: certificate required"}}
	front.Box = b
	if err := front.Reload(context.Background(), proxy.Spec{}); err != nil {
		t.Fatalf("Reload() = %v", err)
	}
	if len(b.paused) != 2 {
		t.Errorf("Reload() paused %d times, want two: until your Traefik refuses the hostname to a client with no certificate, the shield is not in effect", len(b.paused))
	}
}

func TestAShieldedHostnameAnswersWithTheOriginCertificateOcelRenews(t *testing.T) {
	t.Parallel()

	front := coolifyTraefik()
	front.Box = &box{spec: proxy.Spec{
		Hostnames: []string{"shop.example.com"},
		Shields:   []proxy.Shield{{Hostname: "shop.example.com", ClientCAs: []string{clientCertificate(t)}, OriginCertificate: origin}},
	}}
	current, err := front.Certificate(context.Background(), "shop.example.com")
	if err != nil {
		t.Fatalf("Certificate() = %v", err)
	}
	if current.Trouble != nil || !strings.Contains(current.Renewal, "ocel") {
		t.Errorf("Certificate() = %+v, want no trouble and a renewal ocel makes: your Traefik orders nothing for a hostname it answers with its origin certificate", current)
	}
}
