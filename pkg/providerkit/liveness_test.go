package providerkit

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

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

func locatedAt(addresses ...string) func(context.Context, string) ([]string, error) {
	return func(context.Context, string) ([]string, error) { return addresses, nil }
}

func trusting() *tls.Config { return &tls.Config{InsecureSkipVerify: true} }

func TestTheLivenessProbeReadsWhichEdgeAnswersOffItsMarkerAndNotItsStatus(t *testing.T) {
	t.Parallel()

	for _, header := range []string{"cloudfront", "cloudflare", ""} {
		probe := &Liveness{Locate: locatedAt(answeringAs(t, header, nil)), TLS: trusting()}
		kind, err := probe.Serving(context.Background(), "cloudfront", "shop.example.com")
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
	at := answeringAs(t, "", func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Host+" "+r.TLS.ServerName)
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		w.Header().Set(edge.HeaderEdge, "cloudflare")
	})
	probe := &Liveness{Locate: locatedAt(at), TLS: trusting()}

	kind, err := probe.Serving(context.Background(), "cloudflare", "shop.example.com")
	if err != nil {
		t.Fatalf("Serving() = %v", err)
	}
	if kind != "" {
		t.Errorf("Serving() = %q, want nothing: the probe followed a redirect and read the edge off wherever it landed", kind)
	}
	if !slices.Equal(asked, []string{"shop.example.com shop.example.com"}) {
		t.Errorf("the front was asked %v, want the hostname as both Host and SNI: an edge routes and terminates on the name, not the address", asked)
	}
}

func TestAWildcardIsProbedOnALabelUnderIt(t *testing.T) {
	t.Parallel()

	var located string
	probe := &Liveness{
		Locate: func(_ context.Context, hostname string) ([]string, error) {
			located = hostname
			return nil, errors.New("no such host")
		},
	}
	if _, err := probe.Serving(context.Background(), "cloudflare", "*.preview.example.com"); err != nil {
		t.Fatal(err)
	}
	if located != edge.ProbeHostname("*.preview.example.com") {
		t.Errorf("the probe located %q, want %q", located, edge.ProbeHostname("*.preview.example.com"))
	}
}

func TestACertificateNothingTrustsIsUnservedAndSaysWhy(t *testing.T) {
	t.Parallel()

	probe := &Liveness{Locate: locatedAt(answeringAs(t, "cloudfront", nil))}
	kind, err := probe.Serving(context.Background(), "cloudfront", "shop.example.com")
	if err != nil {
		t.Fatalf("Serving() = %v, want it reported unserved so the settle keeps waiting", err)
	}
	if kind != "" {
		t.Errorf("Serving() = %q, want nothing: no header is readable off a handshake the client refused", kind)
	}
	if cause := probe.Unreached("shop.example.com"); !strings.Contains(cause, "x509") {
		t.Errorf("Unreached() = %q, want the refused chain", cause)
	}
}

func TestAHostnameNoNameserverAnswersForYetIsUnservedAndSaysWhy(t *testing.T) {
	t.Parallel()

	probe := &Liveness{Locate: func(context.Context, string) ([]string, error) {
		return nil, errors.New("shop.example.com is not on ns1.example.net yet")
	}}
	kind, err := probe.Serving(context.Background(), "cloudfront", "shop.example.com")
	if err != nil || kind != "" {
		t.Fatalf("Serving() = %q, %v, want it unserved", kind, err)
	}
	if cause := probe.Unreached("shop.example.com"); !strings.Contains(cause, "ns1.example.net") {
		t.Errorf("Unreached() = %q, want what located nothing", cause)
	}
}

func TestAProbeTheRunStoppedReportsTheStop(t *testing.T) {
	t.Parallel()

	ctx, stop := context.WithCancel(context.Background())
	stop()
	probe := &Liveness{Locate: func(ctx context.Context, _ string) ([]string, error) { return nil, ctx.Err() }}
	if _, err := probe.Serving(ctx, "cloudfront", "shop.example.com"); !errors.Is(err, context.Canceled) {
		t.Errorf("Serving() under a cancelled context = %v, want the cancellation", err)
	}
}

type recordedLookups struct {
	asked []string
	ns    map[string][]string
	on    map[string]map[string]string
}

func (r *recordedLookups) authority() authority {
	return authority{
		nameservers: func(_ context.Context, zone string) ([]string, error) {
			r.asked = append(r.asked, "system NS "+zone)
			if held, ok := r.ns[zone]; ok {
				return held, nil
			}
			return nil, &net.DNSError{Err: "no such host", Name: zone, IsNotFound: true}
		},
		answer: func(_ context.Context, nameserver, hostname string) (string, []string, error) {
			r.asked = append(r.asked, nameserver+" "+hostname)
			said, ok := r.on[nameserver][hostname]
			if !ok {
				return "", nil, &net.DNSError{Err: "no such host", Name: hostname, IsNotFound: true}
			}
			if strings.HasPrefix(said, "cname ") {
				return strings.TrimPrefix(said, "cname "), nil, nil
			}
			return hostname, []string{said}, nil
		},
		public: func(_ context.Context, hostname string) ([]string, error) {
			r.asked = append(r.asked, "system A "+hostname)
			if hostname == "d111.cloudfront.net" {
				return []string{"198.51.100.7"}, nil
			}
			return nil, &net.DNSError{Err: "no such host", Name: hostname, IsNotFound: true}
		},
	}
}

func TestAHostnameIsLocatedOnItsZonesOwnNameserversAndNeverThroughTheLocalResolver(t *testing.T) {
	t.Parallel()

	lookups := &recordedLookups{
		ns: map[string][]string{"example.com": {"ns1.example.net", "ns2.example.net"}},
		on: map[string]map[string]string{
			"ns1.example.net": {"shop.example.com": "203.0.113.4"},
			"ns2.example.net": {"shop.example.com": "203.0.113.4"},
		},
	}
	located, err := lookups.authority().locate(context.Background(), "shop.example.com")
	if err != nil {
		t.Fatalf("locate() = %v", err)
	}
	if !slices.Equal(located, []string{"203.0.113.4:443"}) {
		t.Errorf("locate() = %v, want the address the zone's nameservers hold", located)
	}
	for _, asked := range lookups.asked {
		if strings.HasPrefix(asked, "system") && strings.Contains(asked, "shop.example.com") {
			t.Errorf("the local resolver was asked %q: a name asked for before its record lands is cached as absent for the zone's negative ttl, and every browser behind that resolver is refused long after the hostname serves", asked)
		}
	}
}

func TestAHostnameMissingFromOneNameserverIsNotLocatedYet(t *testing.T) {
	t.Parallel()

	lookups := &recordedLookups{
		ns: map[string][]string{"example.com": {"ns1.example.net", "ns2.example.net"}},
		on: map[string]map[string]string{
			"ns1.example.net": {"shop.example.com": "203.0.113.4"},
		},
	}
	if located, err := lookups.authority().locate(context.Background(), "shop.example.com"); err == nil {
		t.Errorf("locate() = %v, want it refused while ns2.example.net has not the record: a resolver asking that one caches the name as absent", located)
	}
}

func TestAHostnameAliasedToTheFrontIsLocatedWhereTheFrontIs(t *testing.T) {
	t.Parallel()

	lookups := &recordedLookups{
		ns: map[string][]string{"example.com": {"ns1.example.net"}},
		on: map[string]map[string]string{
			"ns1.example.net": {"shop.example.com": "cname d111.cloudfront.net."},
		},
	}
	located, err := lookups.authority().locate(context.Background(), "shop.example.com")
	if err != nil {
		t.Fatalf("locate() = %v", err)
	}
	if !slices.Equal(located, []string{"198.51.100.7:443"}) {
		t.Errorf("locate() = %v, want where the alias the zone holds resolves", located)
	}
}

func TestAHostnameTwoLabelsBelowItsZoneFindsTheZoneAboveIt(t *testing.T) {
	t.Parallel()

	lookups := &recordedLookups{
		ns: map[string][]string{"example.com": {"ns1.example.net"}},
		on: map[string]map[string]string{
			"ns1.example.net": {"a.b.example.com": "203.0.113.9"},
		},
	}
	located, err := lookups.authority().locate(context.Background(), "a.b.example.com")
	if err != nil {
		t.Fatalf("locate() = %v", err)
	}
	if !slices.Equal(located, []string{"203.0.113.9:443"}) {
		t.Errorf("locate() = %v", located)
	}
}

func TestAnAttendedSettleWaitsOutAFrontThatTakesMinutesToAnswer(t *testing.T) {
	t.Parallel()

	const minutes = 10 * time.Minute
	front := frontOf{kind: "relay"}
	for what, attended := range map[string]bool{"domain add": true, "a deploy": false} {
		settle := newSettler(front, nil, "", &answering{kind: "relay", after: int(minutes / settleWait)})
		if attended {
			settle.attend(nil)
		}
		settle.sleep = func(context.Context, time.Duration) error { return nil }
		_, err := settle.await(context.Background(), "shop.example.com", func(string) {})
		if attended && err != nil {
			t.Errorf("%s gave up on a front that answers after %s: %v. A fresh CloudFront distribution takes minutes to serve, and `ocel domain add` is the command that waits for it", what, minutes, err)
		}
		if !attended && err == nil {
			t.Errorf("%s waited %s for one hostname: a deploy leaves a slow hostname pending for `ocel domain add` rather than holding the release", what, minutes)
		}
	}
}
