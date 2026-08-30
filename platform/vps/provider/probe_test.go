package vps_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

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
