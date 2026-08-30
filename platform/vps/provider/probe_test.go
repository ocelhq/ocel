package vps_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const probeCeiling = 15 * time.Second

func probingAt(t *testing.T, header string) *vps.Provider {
	t.Helper()

	served := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			t.Error("the probe reached the hostname over something other than tls, and a header read off plain http proves no certificate was ever served")
		}
		if header != "" {
			w.Header().Set(edge.HeaderEdge, header)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(served.Close)

	at, err := url.Parse(served.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, at.Host)
		},
	}}

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	p.Probing(client)
	return p
}

func TestTheBoxAnswersWhichEdgeServesAHostnameOffTheHeaderItReadsOverTls(t *testing.T) {
	t.Parallel()

	for what, header := range map[string]string{
		"the box itself":    string(boxedge.Kind),
		"a different edge":  "cloudfront",
		"nothing ocel runs": "",
	} {
		kind, err := probingAt(t, header).Serving(context.Background(), boxedge.Kind, "shop.example.com")
		if err != nil {
			t.Fatalf("Serving() over %s = %v", what, err)
		}
		if string(kind) != header {
			t.Errorf("Serving() over %s = %q, want %q read off %s", what, kind, header, edge.HeaderEdge)
		}
	}
}

func TestTheEdgeIsReadOffTheHostnameProbedAndNotOffWhereeverItPointsOn(t *testing.T) {
	t.Parallel()

	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(edge.HeaderEdge, "cloudfront")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(elsewhere.Close)

	fronting := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/", http.StatusFound)
	}))
	t.Cleanup(fronting.Close)

	at, err := url.Parse(fronting.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, asked string) (net.Conn, error) {
			if strings.HasPrefix(asked, at.Hostname()) {
				return (&net.Dialer{}).DialContext(ctx, network, asked)
			}
			return (&net.Dialer{}).DialContext(ctx, network, at.Host)
		},
	}}
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	p.Probing(client)

	kind, err := p.Serving(context.Background(), boxedge.Kind, "shop.example.com")
	if err != nil {
		t.Fatalf("Serving() over a hostname fronted by a redirect = %v", err)
	}
	if kind != "" {
		t.Errorf("Serving() = %q, want nothing: the probe followed a redirect and read the edge off wherever the chain landed, then attributed it to shop.example.com", kind)
	}
}

func TestAHostnameServingACertificateNothingTrustsKeepsConvergingAndSaysWhy(t *testing.T) {
	t.Parallel()

	served := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(edge.HeaderEdge, string(boxedge.Kind))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(served.Close)

	at, err := url.Parse(served.URL)
	if err != nil {
		t.Fatal(err)
	}
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	p.Probing(&http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, at.Host)
		},
	}})

	kind, err := p.Serving(context.Background(), boxedge.Kind, "shop.example.com")
	if err != nil {
		t.Fatalf("Serving() over a certificate nothing trusts = %v, want it reported unserved: a settle writes the record and probes at once, so for the whole of the old record's ttl the probe reaches the previous host, and a deploy that dies on attempt 1 there never moves the domain at all",
			err)
	}
	if kind != "" {
		t.Errorf("Serving() = %q, want nothing: no header is readable off a handshake the client refused", kind)
	}
	if cause := p.Unreached("shop.example.com"); !strings.Contains(cause, "x509") {
		t.Errorf("Unreached() = %q, want the chain the client refused: the settle gives up after a full minute with nothing for the operator to act on", cause)
	}
}

func TestAHostnameThatAnswersClearsTheCauseTheLastAttemptLeft(t *testing.T) {
	t.Parallel()

	served := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(edge.HeaderEdge, string(boxedge.Kind))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(served.Close)

	at, err := url.Parse(served.URL)
	if err != nil {
		t.Fatal(err)
	}
	refused := true
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	p.Probing(&http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			if refused {
				refused = false
				return nil, errors.New("connect: connection refused")
			}
			return (&net.Dialer{}).DialContext(ctx, network, at.Host)
		},
	}})

	if _, err := p.Serving(context.Background(), boxedge.Kind, "shop.example.com"); err != nil {
		t.Fatal(err)
	}
	if p.Unreached("shop.example.com") == "" {
		t.Fatal("a hostname the probe never reached carries no cause, and this test states nothing about clearing one")
	}
	if _, err := p.Serving(context.Background(), boxedge.Kind, "shop.example.com"); err != nil {
		t.Fatal(err)
	}
	if cause := p.Unreached("shop.example.com"); cause != "" {
		t.Errorf("Unreached() = %q for a hostname that answered, and a stale cause is read out on whatever the settle gives up on next", cause)
	}
}

func TestAProbeTheRunGaveUpOnSaysSoRatherThanReportingTheHostnameUnserved(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	p.Probing(&http.Client{Timeout: probeCeiling, Transport: &http.Transport{}})

	ctx, stop := context.WithCancel(context.Background())
	stop()

	if _, err := p.Serving(ctx, boxedge.Kind, "shop.example.com"); !errors.Is(err, context.Canceled) {
		t.Errorf("Serving() under a cancelled context = %v, want the cancellation: a deploy the user stopped reads as a hostname that does not answer yet", err)
	}
}

func TestAHostnameNothingAnswersIsNotAnErrorTheSettleGivesUpOn(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	p.Probing(&http.Client{Timeout: probeCeiling, Transport: &http.Transport{}})

	kind, err := p.Serving(context.Background(), boxedge.Kind, "nothing.invalid")
	if err != nil {
		t.Fatalf("Serving() over a hostname that resolves to nothing = %v, want it reported as unserved: the settle retries on an empty answer and gives up on an error", err)
	}
	if kind != "" {
		t.Errorf("Serving() = %q, want nothing", kind)
	}
}

func edgeNamed(t *testing.T, rendered []byte, hostname string) http.Header {
	t.Helper()

	var read struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Match []struct {
							Host []string `json:"host"`
						} `json:"match"`
						Handle []struct {
							Handler   string `json:"handler"`
							Upstreams []struct {
								Dial string `json:"dial"`
							} `json:"upstreams"`
							Response struct {
								Set map[string][]string `json:"set"`
							} `json:"response"`
						} `json:"handle"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(rendered, &read); err != nil {
		t.Fatal(err)
	}
	said := http.Header{}
	for _, server := range read.Apps.HTTP.Servers {
		for _, route := range server.Routes {
			forwards := false
			for _, handled := range route.Handle {
				forwards = forwards || len(handled.Upstreams) > 0
			}
			matches := false
			for _, match := range route.Match {
				matches = matches || slices.Contains(match.Host, hostname)
			}
			if !forwards || !matches {
				continue
			}
			for _, handled := range route.Handle {
				for name, values := range handled.Response.Set {
					for _, value := range values {
						said.Add(name, value)
					}
				}
			}
		}
	}
	return said
}

func TestAHostnameOneOfTheBoxesProjectsAnswersStillNamesTheBoxAsItsEdge(t *testing.T) {
	t.Parallel()

	const hostname = "shop.example.com"
	const owner = "ocel--shop--production"
	rendered, err := host.RenderProxyConfig(host.ProxyState{
		Grace:  host.DrainWindow,
		Claims: []host.HostClaim{{Hostname: hostname, Owner: owner}},
		Routes: []host.AppRoute{{
			RouteKey: host.RouteKey{Owner: owner, Pointer: "@production", App: "web"},
			Upstream: "shop-web-2222:" + host.AppPort,
		}},
	})
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}

	served := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, values := range edgeNamed(t, rendered, hostname) {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		_, _ = w.Write([]byte("the body of the app the project runs"))
	}))
	t.Cleanup(served.Close)

	at, err := url.Parse(served.URL)
	if err != nil {
		t.Fatal(err)
	}
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	p.Probing(&http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, at.Host)
		},
	}})

	kind, err := p.Serving(context.Background(), boxedge.Kind, hostname)
	if err != nil {
		t.Fatalf("Serving() = %v", err)
	}
	if kind != boxedge.Kind {
		t.Errorf("Serving() over a hostname a project on the box claims and routes = %q, want %q: the box names the edge only on the route nothing claims, so the first bind on a project already deployed probes its own app, reads no header and burns every attempt before refusing", kind, boxedge.Kind)
	}
}
