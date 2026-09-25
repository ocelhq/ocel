package vps_test

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	trusted   = true
	untrusted = false
)

type resolvesEverywhere struct{ resolvesNothing }

func (resolvesEverywhere) LookupHost(context.Context, string) ([]string, error) {
	return []string{"192.0.2.1"}, nil
}

type resolvesNothing struct{}

func (resolvesNothing) LookupHost(_ context.Context, host string) ([]string, error) {
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (resolvesNothing) LookupNS(_ context.Context, name string) ([]*net.NS, error) {
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (resolvesNothing) LookupCNAME(_ context.Context, host string) (string, error) {
	return "", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func probedAt(p *vps.Provider, at string, trust bool) {
	p.System = resolvesEverywhere{}
	p.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, at)
	}
	if trust {
		p.TLS = &tls.Config{InsecureSkipVerify: true}
	}
}

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
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	probedAt(p, at.Host, trusted)
	return p
}

func TestTheBoxAnswersWhichEdgeServesAHostnameOffTheHeaderItReadsOverTls(t *testing.T) {
	t.Parallel()

	for what, header := range map[string]string{
		"the box itself":    string(boxedge.Kind),
		"a different edge":  "cloudfront",
		"nothing ocel runs": "",
	} {
		kind, err := probingAt(t, header).ServingEdge(context.Background(), boxedge.Kind, "shop.example.com")
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
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	probedAt(p, at.Host, trusted)

	kind, err := p.ServingEdge(context.Background(), boxedge.Kind, "shop.example.com")
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
	probedAt(p, at.Host, untrusted)

	kind, err := p.ServingEdge(context.Background(), boxedge.Kind, "shop.example.com")
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
	probedAt(p, at.Host, trusted)
	p.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		if refused {
			refused = false
			return nil, errors.New("connect: connection refused")
		}
		return (&net.Dialer{}).DialContext(ctx, network, at.Host)
	}

	if _, err := p.ServingEdge(context.Background(), boxedge.Kind, "shop.example.com"); err != nil {
		t.Fatal(err)
	}
	if p.Unreached("shop.example.com") == "" {
		t.Fatal("a hostname the probe never reached carries no cause, and this test states nothing about clearing one")
	}
	if _, err := p.ServingEdge(context.Background(), boxedge.Kind, "shop.example.com"); err != nil {
		t.Fatal(err)
	}
	if cause := p.Unreached("shop.example.com"); cause != "" {
		t.Errorf("Unreached() = %q for a hostname that answered, and a stale cause is read out on whatever the settle gives up on next", cause)
	}
}

func TestAProbeTheRunGaveUpOnSaysSoRatherThanReportingTheHostnameUnserved(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	p.System = resolvesNothing{}

	ctx, stop := context.WithCancel(context.Background())
	stop()

	if _, err := p.ServingEdge(ctx, boxedge.Kind, "shop.example.com"); !errors.Is(err, context.Canceled) {
		t.Errorf("Serving() under a cancelled context = %v, want the cancellation: a deploy the user stopped reads as a hostname that does not answer yet", err)
	}
}

func TestAHostnameNothingAnswersIsNotAnErrorTheSettleGivesUpOn(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	p.System = resolvesNothing{}

	kind, err := p.ServingEdge(context.Background(), boxedge.Kind, "nothing.invalid")
	if err != nil {
		t.Fatalf("Serving() over a hostname that resolves to nothing = %v, want it reported as unserved: the settle retries on an empty answer and gives up on an error", err)
	}
	if kind != "" {
		t.Errorf("Serving() = %q, want nothing", kind)
	}
}

func TestAHostnameOneOfTheBoxesProjectsAnswersStillNamesTheBoxAsItsEdge(t *testing.T) {
	t.Parallel()

	const hostname = "shop.example.com"
	const owner = "ocel--shop--production"
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("the body of the app the project runs"))
	}))
	t.Cleanup(app.Close)
	written, err := host.WriteRoutingTable(host.RoutingTable{
		Grace:  host.DrainWindow,
		Claims: []host.HostClaim{{Hostname: hostname, Owner: owner, Pointer: edge.DefaultPointer}},
		Routes: []host.AppRoute{{
			RouteKey: host.RouteKey{Owner: owner, Pointer: "@production", App: "web"},
			Upstream: strings.TrimPrefix(app.URL, "http://"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	table, err := switchboard.Read(written)
	if err != nil {
		t.Fatalf("switchboard.Read() = %v", err)
	}
	served := httptest.NewTLSServer(switchboard.New(table))
	t.Cleanup(served.Close)

	at, err := url.Parse(served.URL)
	if err != nil {
		t.Fatal(err)
	}
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	probedAt(p, at.Host, trusted)

	kind, err := p.ServingEdge(context.Background(), boxedge.Kind, hostname)
	if err != nil {
		t.Fatalf("Serving() = %v", err)
	}
	if kind != boxedge.Kind {
		t.Errorf("Serving() over a hostname a project on the box claims and routes = %q, want %q: a box that names the edge only on the route nothing claims makes the first bind on a project already deployed probe its own app, read no header and burn every attempt before refusing", kind, boxedge.Kind)
	}
}

func probedOnTheBox(t *testing.T, answer session.Result) (*vps.Provider, *box) {
	t.Helper()

	machine := &box{refuses: func(command string) (session.Result, bool) {
		if strings.Contains(command, "ocel-switchboard' 'probe'") {
			return answer, true
		}
		return session.Result{}, false
	}}
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	p.Dial = func(context.Context, string, string) (net.Conn, error) {
		t.Error("a .localhost name was dialled from the operator's machine, where RFC 6761 resolves it to this machine's own loopback and never to the box")
		return nil, errors.New("dialled from here")
	}
	return p, machine
}

func TestALocalhostNameIsProbedOnTheBoxItResolvesOn(t *testing.T) {
	t.Parallel()

	p, machine := probedOnTheBox(t, session.Result{Stdout: string(boxedge.Kind) + "\n"})

	kind, err := p.ServingEdge(context.Background(), boxedge.Kind, "web.localhost")
	if err != nil {
		t.Fatalf("Serving() = %v", err)
	}
	if kind != boxedge.Kind {
		t.Errorf("Serving() = %q, want %q read off the box's own proxy", kind, boxedge.Kind)
	}
	if machine.at("ocel-switchboard' 'probe' 'web.localhost'") < 0 {
		t.Errorf("the box was never asked to probe web.localhost: %v", machine.commands())
	}
}

func TestALocalhostNameTheBoxCannotReachKeepsConvergingAndSaysWhy(t *testing.T) {
	t.Parallel()

	p, _ := probedOnTheBox(t, session.Result{Code: 3,
		Stderr: "web.localhost at 127.0.0.1:443: the certificate served is for fallback.localhost, not this name"})

	kind, err := p.ServingEdge(context.Background(), boxedge.Kind, "web.localhost")
	if err != nil {
		t.Fatalf("Serving() = %v, want it reported unserved: the proxy obtains the name's certificate in the background after the bind", err)
	}
	if kind != "" {
		t.Errorf("Serving() = %q, want nothing", kind)
	}
	if cause := p.Unreached("web.localhost"); !strings.Contains(cause, "fallback.localhost") {
		t.Errorf("Unreached() = %q, want what stopped the probe on the box", cause)
	}
}

func TestALocalhostProbeTheBoxRefusesIsAnError(t *testing.T) {
	t.Parallel()

	p, _ := probedOnTheBox(t, session.Result{Code: 2, Stderr: "usage: ocel-switchboard serve"})

	if _, err := p.ServingEdge(context.Background(), boxedge.Kind, "web.localhost"); err == nil {
		t.Error("Serving() = nil over a proxy that could not be asked at all, and the settle burns a minute on a box whose proxy is down")
	}
}
