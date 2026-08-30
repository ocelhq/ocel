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

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
)

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

func TestAHostnameServingACertificateNothingTrustsIsRefusedRatherThanReportedUnserved(t *testing.T) {
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

	var refusal providerkit.Refusal
	kind, err := p.Serving(context.Background(), boxedge.Kind, "shop.example.com")
	if !errors.As(err, &refusal) {
		t.Fatalf("Serving() = %q, %v, want a refusal naming the chain: a hostname whose certificate nothing trusts reads as one that does not answer yet, and the run gives up after a full minute with the cause thrown away", kind, err)
	}
	if !strings.Contains(refusal.Message, "shop.example.com") {
		t.Errorf("Serving() refused with %q, want it to name the hostname the chain was refused for", refusal.Message)
	}
}

func TestAProbeTheRunGaveUpOnSaysSoRatherThanReportingTheHostnameUnserved(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	p.Probing(&http.Client{Transport: &http.Transport{}})

	ctx, stop := context.WithCancel(context.Background())
	stop()

	if _, err := p.Serving(ctx, boxedge.Kind, "shop.example.com"); !errors.Is(err, context.Canceled) {
		t.Errorf("Serving() under a cancelled context = %v, want the cancellation: a deploy the user stopped reads as a hostname that does not answer yet", err)
	}
}

func TestAHostnameNothingAnswersIsNotAnErrorTheSettleGivesUpOn(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	p.Probing(&http.Client{Transport: &http.Transport{}})

	kind, err := p.Serving(context.Background(), boxedge.Kind, "nothing.invalid")
	if err != nil {
		t.Fatalf("Serving() over a hostname that resolves to nothing = %v, want it reported as unserved: the settle retries on an empty answer and gives up on an error", err)
	}
	if kind != "" {
		t.Errorf("Serving() = %q, want nothing", kind)
	}
}
