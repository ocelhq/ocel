package host

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

const publicPreviewBase = "preview.example.com"

func refusePreviewCertificates(t *testing.T, state RoutingTable, front Front, pins []Pin) error {
	t.Helper()
	return refusePreviewCertificatesDeployingTo(t, state, front, pins, "")
}

func refusePreviewCertificatesDeployingTo(t *testing.T, state RoutingTable, front Front, pins []Pin, destination string) error {
	t.Helper()
	state.Grace = 30 * time.Second
	table := string(mustWrite(t, state))
	b := machine(nil)
	b.answer = servesProxy(b, &table)
	h := New(b.dial, Keys{}, pins, front)
	return h.RefusePerHostnamePreviewCertificates(context.Background(), destination)
}

func newTraefikFront(previewResolver string) Front {
	return Front{Traefik: &TraefikFront{Directory: "/etc/traefik/dynamic", Resolver: "le", PreviewResolver: previewResolver}}
}

func TestAPreviewWhoseHostnamesEachGetAPublicCertificateIsRefusedNamingEveryWayOut(t *testing.T) {
	t.Parallel()

	for name, front := range map[string]Front{
		"ocel's own proxy":           {},
		"a Traefik with no wildcard": newTraefikFront(""),
		"your Caddy":                 {Caddy: &CaddyFront{Directory: "/etc/caddy/ocel", Config: "/etc/caddy/Caddyfile"}},
	} {
		err := refusePreviewCertificates(t, RoutingTable{PreviewBase: publicPreviewBase}, front, nil)
		if err == nil {
			t.Errorf("%s: a preview on %s was let through, and every one of its hostnames lands in Certificate Transparency logs within minutes", name, publicPreviewBase)
			continue
		}
		for _, want := range []string{"Certificate Transparency", "edge", "perHostnamePreviewCertificates", publicPreviewBase} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal reads %q, want it to name %q", name, err, want)
			}
		}
	}
}

func TestATraefikPreviewIsRefusedNamingThePreviewResolverThatWouldShareOneCertificate(t *testing.T) {
	t.Parallel()

	err := refusePreviewCertificates(t, RoutingTable{PreviewBase: publicPreviewBase}, newTraefikFront(""), nil)
	if err == nil || !strings.Contains(err.Error(), "previewResolver") {
		t.Errorf("the refusal reads %v, want it to name previewResolver", err)
	}
}

func TestAPreviewServedUnderOneWildcardCertificateIsNotRefused(t *testing.T) {
	t.Parallel()

	origin := proxy.CertificatePair{Certificate: "ORIGIN", Key: "KEY"}
	for name, bench := range map[string]struct {
		state RoutingTable
		front Front
		pins  []Pin
	}{
		"a Traefik with a preview resolver": {state: RoutingTable{PreviewBase: publicPreviewBase}, front: newTraefikFront("dns")},
		"a proxy you route yourself":        {state: RoutingTable{PreviewBase: publicPreviewBase}, front: Front{Manual: &ManualFront{Port: 8480}}},
		"a pinned wildcard":                 {state: RoutingTable{PreviewBase: publicPreviewBase}, pins: []Pin{{Hostname: "*." + publicPreviewBase, Path: caddy.PinsDir + "/preview"}}},
		"an edge's origin certificate": {state: RoutingTable{PreviewBase: publicPreviewBase, Shields: []Shield{{
			Hostname: "*." + publicPreviewBase, Owner: "ocel-preview-entry", ClientCAs: []string{"zone"}, OriginCertificate: origin,
		}}}},
		"a base no public authority issues for": {state: RoutingTable{PreviewBase: "preview.ocel.home.arpa"}},
		"no preview base on the box":            {state: RoutingTable{}},
	} {
		if err := refusePreviewCertificates(t, bench.state, bench.front, bench.pins); err != nil {
			t.Errorf("%s: %v, want the preview let through: no preview hostname gets a publicly logged certificate of its own", name, err)
		}
	}
}

func TestAnEdgeThatShieldsThePreviewsWithoutAnOriginCertificateStillLeavesEachHostnameItsOwn(t *testing.T) {
	t.Parallel()

	err := refusePreviewCertificates(t, RoutingTable{PreviewBase: publicPreviewBase, Shields: []Shield{{
		Hostname: "*." + publicPreviewBase, Owner: "ocel-preview-entry", ClientCAs: []string{"zone"},
	}}}, Front{}, nil)
	if err == nil {
		t.Error("a shield with no origin certificate let the preview through, and the box still orders a public certificate per hostname behind it")
	}
}

func TestTheFirstPreviewOnABoxIsRefusedByTheBaseItDeploysTo(t *testing.T) {
	t.Parallel()

	err := refusePreviewCertificatesDeployingTo(t, RoutingTable{}, Front{}, nil, publicPreviewBase)
	if err == nil || !strings.Contains(err.Error(), publicPreviewBase) {
		t.Errorf("the first preview on an empty box was let through with %v, want it refused by %s, the base it installs", err, publicPreviewBase)
	}
}
