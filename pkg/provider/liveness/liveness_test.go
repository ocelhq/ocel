package liveness

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func answeringAs(t *testing.T, header string, handler http.HandlerFunc) string {
	t.Helper()
	if handler == nil {
		handler = func(w http.ResponseWriter, _ *http.Request) {
			if header != "" {
				w.Header().Set(edge.HeaderEdge, header)
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}
	served := httptest.NewTLSServer(handler)
	t.Cleanup(served.Close)
	return served.Listener.Addr().String()
}

type dnsBook struct {
	mu      sync.Mutex
	asked   []string
	down    bool
	hosts   map[string][]string
	aliases map[string]string
	zones   map[string][]string
}

func (b *dnsBook) note(what, name string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	name = strings.TrimSuffix(name, ".")
	b.asked = append(b.asked, what+" "+name)
	return name
}

func (b *dnsBook) heard() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.asked)
}

func missing(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (b *dnsBook) Host(_ context.Context, host string) ([]string, error) {
	host = b.note("A", host)
	if b.down {
		return nil, &net.DNSError{Err: "i/o timeout", Name: host, IsTimeout: true}
	}
	if addresses, ok := b.hosts[host]; ok {
		return addresses, nil
	}
	return nil, missing(host)
}

func (b *dnsBook) CNAME(_ context.Context, host string) (string, error) {
	host = b.note("CNAME", host)
	if b.down {
		return "", &net.DNSError{Err: "i/o timeout", Name: host, IsTimeout: true}
	}
	if target, ok := b.aliases[host]; ok {
		return target + ".", nil
	}
	if _, ok := b.hosts[host]; ok {
		return host + ".", nil
	}
	return "", missing(host)
}

func (b *dnsBook) NS(_ context.Context, name string) ([]*net.NS, error) {
	name = b.note("NS", name)
	servers, ok := b.zones[name]
	if !ok {
		return nil, missing(name)
	}
	named := make([]*net.NS, 0, len(servers))
	for _, ns := range servers {
		named = append(named, &net.NS{Host: ns + "."})
	}
	return named, nil
}

func dialing(routes map[string]string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		at, ok := routes[address]
		if !ok {
			return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("connection refused")}
		}
		return (&net.Dialer{}).DialContext(ctx, network, at)
	}
}

func trusting() *tls.Config { return &tls.Config{InsecureSkipVerify: true} }

func resolvedAt(t *testing.T, header string, handler http.HandlerFunc) *Net {
	t.Helper()
	return &Net{
		System: &dnsBook{hosts: map[string][]string{"shop.example.com": {"203.0.113.4"}}},
		Dial:   dialing(map[string]string{"203.0.113.4:443": answeringAs(t, header, handler)}),
		TLS:    trusting(),
	}
}

func TestTheLivenessProbeReadsWhichEdgeAnswersOffItsMarkerAndNotItsStatus(t *testing.T) {
	t.Parallel()

	for _, header := range []string{"cloudfront", "cloudflare", ""} {
		probe := resolvedAt(t, header, nil)
		kind, err := probe.ServingEdge(context.Background(), "cloudfront", "shop.example.com")
		if err != nil {
			t.Fatalf("Serving() = %v", err)
		}
		if string(kind) != header {
			t.Errorf("Serving() = %q, want %q read off %s: a placeholder answering 404 is a front that serves the hostname", kind, header, edge.HeaderEdge)
		}
	}
}

func TestTheLivenessProbeAsksForTheHostnameItProbesAndFollowsNoRedirect(t *testing.T) {
	t.Parallel()

	var asked []string
	probe := resolvedAt(t, "", func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Host+" "+r.TLS.ServerName+" "+r.URL.Path)
		if r.URL.Path == edge.LivenessProbePath {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		w.Header().Set(edge.HeaderEdge, "cloudflare")
	})

	kind, err := probe.ServingEdge(context.Background(), "cloudflare", "shop.example.com")
	if err != nil {
		t.Fatalf("Serving() = %v", err)
	}
	if kind != "" {
		t.Errorf("Serving() = %q, want nothing: the probe followed a redirect and read the edge off wherever it landed", kind)
	}
	if !slices.Equal(asked, []string{"shop.example.com shop.example.com " + edge.LivenessProbePath}) {
		t.Errorf("the front was asked %v, want the hostname as both Host and SNI, on the path every front answers itself: an edge routes and terminates on the name, not the address, and an app behind API Gateway's proxy integration answers / without the marker", asked)
	}
}

func TestAHostnameTheSystemResolverAnswersIsProbedWhereItResolvesAndNoNameserverIsAsked(t *testing.T) {
	t.Parallel()

	probe := resolvedAt(t, "cloudfront", nil)
	var authorities []string
	probe.Authority = func(nameserver string) DNSLookup {
		authorities = append(authorities, nameserver)
		return &dnsBook{}
	}

	kind, err := probe.ServingEdge(context.Background(), "cloudfront", "shop.example.com")
	if err != nil || kind != "cloudfront" {
		t.Fatalf("Serving() = %q, %v, want the edge at the address the system resolver gave", kind, err)
	}
	if len(authorities) != 0 {
		t.Errorf("the probe asked nameservers %v directly although the system resolver answered: a private zone, a split horizon or a hosts-file entry is what the operator's own machine reaches, and the public nameservers know nothing of it", authorities)
	}
}

func TestAWildcardIsProbedOnALabelUnderIt(t *testing.T) {
	t.Parallel()

	book := &dnsBook{}
	probe := &Net{System: book, Authority: func(string) DNSLookup { return &dnsBook{} }}
	if _, err := probe.ServingEdge(context.Background(), "cloudflare", "*.preview.example.com"); err != nil {
		t.Fatal(err)
	}
	want := "A " + edge.ProbeHostname("*.preview.example.com")
	if heard := book.heard(); len(heard) == 0 || heard[0] != want {
		t.Errorf("the probe asked %v first, want %q", heard, want)
	}
}

func TestACertificateNothingTrustsIsUnservedAndSaysWhy(t *testing.T) {
	t.Parallel()

	probe := resolvedAt(t, "cloudfront", nil)
	probe.TLS = nil
	kind, err := probe.ServingEdge(context.Background(), "cloudfront", "shop.example.com")
	if err != nil {
		t.Fatalf("Serving() = %v, want it reported unserved so the cutover wait keeps polling", err)
	}
	if kind != "" {
		t.Errorf("Serving() = %q, want nothing: no header is readable off a handshake the client refused", kind)
	}
	if cause := probe.LastProbeFailure("shop.example.com"); !strings.Contains(cause, "x509") {
		t.Errorf("LastProbeFailure() = %q, want the refused chain", cause)
	}
}

func hinted(t *testing.T, zones map[string][]string, nameservers map[string]*dnsBook) *Net {
	t.Helper()
	return &Net{
		System: &dnsBook{
			zones: zones,
			hosts: map[string][]string{"d111.cloudfront.net": {"198.51.100.7"}},
		},
		Authority: func(nameserver string) DNSLookup {
			if book, ok := nameservers[nameserver]; ok {
				return book
			}
			return &dnsBook{down: true}
		},
		Dial: dialing(map[string]string{
			"203.0.113.4:443":  answeringAs(t, "cloudfront", nil),
			"198.51.100.7:443": answeringAs(t, "cloudfront", nil),
		}),
		TLS: trusting(),
	}
}

func TestAHostnameTheSystemResolverCannotSeeYetIsLocatedOffAnyOneOfItsZonesNameservers(t *testing.T) {
	t.Parallel()

	probe := hinted(t,
		map[string][]string{"example.com": {"ns1.example.net", "ns2.example.net"}},
		map[string]*dnsBook{
			"ns1.example.net": {down: true},
			"ns2.example.net": {hosts: map[string][]string{"shop.example.com": {"203.0.113.4"}}},
		})

	kind, err := probe.ServingEdge(context.Background(), "cloudfront", "shop.example.com")
	if err != nil || kind != "cloudfront" {
		t.Fatalf("Serving() = %q, %v (%s), want the edge located off the one nameserver that answered: the address is a hint, and one nameserver out of reach is no reason to call the hostname unserved", kind, err, probe.LastProbeFailure("shop.example.com"))
	}
}

func TestAHostnameThatIsItsOwnZoneApexIsAskedOfItsOwnNameservers(t *testing.T) {
	t.Parallel()

	probe := hinted(t,
		map[string][]string{"shop.example.co.uk": {"ns1.example.net"}, "co.uk": {"ns.nic.uk"}},
		map[string]*dnsBook{
			"ns1.example.net": {hosts: map[string][]string{"shop.example.co.uk": {"203.0.113.4"}}},
		})

	kind, err := probe.ServingEdge(context.Background(), "cloudfront", "shop.example.co.uk")
	if err != nil || kind != "cloudfront" {
		t.Fatalf("Serving() = %q, %v (%s), want the apex located off the zone it heads", kind, err, probe.LastProbeFailure("shop.example.co.uk"))
	}
}

func TestAHostnameTwoLabelsBelowItsZoneFindsTheZoneAboveIt(t *testing.T) {
	t.Parallel()

	probe := hinted(t,
		map[string][]string{"example.com": {"ns1.example.net"}},
		map[string]*dnsBook{
			"ns1.example.net": {hosts: map[string][]string{"a.b.example.com": {"203.0.113.4"}}},
		})

	if kind, err := probe.ServingEdge(context.Background(), "cloudfront", "a.b.example.com"); err != nil || kind != "cloudfront" {
		t.Fatalf("Serving() = %q, %v (%s)", kind, err, probe.LastProbeFailure("a.b.example.com"))
	}
}

func TestAHostnameAliasedToTheFrontIsLocatedWhereTheFrontIs(t *testing.T) {
	t.Parallel()

	probe := hinted(t,
		map[string][]string{"example.com": {"ns1.example.net"}},
		map[string]*dnsBook{
			"ns1.example.net": {aliases: map[string]string{"shop.example.com": "d111.cloudfront.net"}},
		})

	if kind, err := probe.ServingEdge(context.Background(), "cloudfront", "shop.example.com"); err != nil || kind != "cloudfront" {
		t.Fatalf("Serving() = %q, %v (%s), want the edge where the alias the zone records resolves", kind, err, probe.LastProbeFailure("shop.example.com"))
	}
}

func TestAHostnameNoNameserverAnswersForYetIsUnservedAndSaysWhy(t *testing.T) {
	t.Parallel()

	probe := hinted(t,
		map[string][]string{"example.com": {"ns1.example.net", "ns2.example.net"}},
		map[string]*dnsBook{"ns1.example.net": {}, "ns2.example.net": {}})

	kind, err := probe.ServingEdge(context.Background(), "cloudfront", "shop.example.com")
	if err != nil || kind != "" {
		t.Fatalf("Serving() = %q, %v, want it unserved", kind, err)
	}
	cause := probe.LastProbeFailure("shop.example.com")
	for _, named := range []string{"ns1.example.net", "ns2.example.net", "example.com"} {
		if !strings.Contains(cause, named) {
			t.Errorf("LastProbeFailure() = %q, want it to name %s", cause, named)
		}
	}
}

func TestAProbeTheRunStoppedReportsTheStop(t *testing.T) {
	t.Parallel()

	ctx, stop := context.WithCancel(context.Background())
	stop()
	probe := resolvedAt(t, "cloudfront", nil)
	if _, err := probe.ServingEdge(ctx, "cloudfront", "shop.example.com"); !errors.Is(err, context.Canceled) {
		t.Errorf("Serving() under a cancelled context = %v, want the cancellation", err)
	}
}

func TestAFrontThatAnswersAtAKnownEndpointIsAskedThereForTheHostname(t *testing.T) {
	t.Parallel()

	var asked []string
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Host)
		w.Header().Set(edge.HeaderEdge, "api-gateway")
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(served.Close)
	front, err := url.Parse(served.URL)
	if err != nil {
		t.Fatal(err)
	}
	book := &dnsBook{}
	probe := &Net{System: book, ProbeAddress: front}

	kind, err := probe.ServingEdge(context.Background(), "api-gateway", "web.journey.test")
	if err != nil || kind != "api-gateway" {
		t.Fatalf("Serving() = %q, %v (%s), want the edge the endpoint answered as", kind, err, probe.LastProbeFailure("web.journey.test"))
	}
	if !slices.Equal(asked, []string{"web.journey.test"}) {
		t.Errorf("the endpoint was asked for %v, want the hostname: the front routes on the name", asked)
	}
	if heard := book.heard(); len(heard) != 0 {
		t.Errorf("the probe asked DNS %v, want nothing: where the front answers is already known", heard)
	}
}

func TestAFrontThatAnswersOnlyOnItsOwnMachineIsProbedThereForEveryName(t *testing.T) {
	t.Parallel()

	book := &dnsBook{}
	var asked []string
	probe := &Net{
		System:       book,
		LoopbackOnly: true,
		Loopback: func(_ context.Context, hostname string) (edge.Kind, error) {
			asked = append(asked, hostname)
			return "box", nil
		},
	}
	kind, err := probe.ServingEdge(context.Background(), "box", "shop.example.com")
	if err != nil || kind != "box" {
		t.Fatalf("Serving() = %q, %v, want box", kind, err)
	}
	if !slices.Equal(asked, []string{"shop.example.com"}) {
		t.Errorf("the loopback probe was asked %v, want the public name asked there too", asked)
	}
	if heard := book.heard(); len(heard) != 0 {
		t.Errorf("the probe asked DNS %v, want nothing: the machine is asked for the name directly", heard)
	}
}

func TestALoopbackNameIsProbedFromWhereItResolves(t *testing.T) {
	t.Parallel()

	var asked []string
	probe := &Net{
		System: &dnsBook{},
		Loopback: func(_ context.Context, hostname string) (edge.Kind, error) {
			asked = append(asked, hostname)
			return "", ProbeUnanswered{Cause: "tls: no certificate for " + hostname + " yet"}
		},
	}
	kind, err := probe.ServingEdge(context.Background(), "box", "shop.localhost")
	if err != nil || kind != "" {
		t.Fatalf("Serving() = %q, %v, want it unserved", kind, err)
	}
	if !slices.Equal(asked, []string{"shop.localhost"}) {
		t.Errorf("the loopback probe was asked %v", asked)
	}
	if cause := probe.LastProbeFailure("shop.localhost"); !strings.Contains(cause, "no certificate") {
		t.Errorf("LastProbeFailure() = %q, want what the loopback probe said", cause)
	}
	if _, err := probe.ServingEdge(context.Background(), "box", "shop.example.com"); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 {
		t.Errorf("a public name went to the loopback probe: %v", asked)
	}
}
