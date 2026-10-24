package host

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const publicPreviewBase = "preview.example.com"

func refusePreviewCertificates(t *testing.T, state boxapi.RoutingTable, fronting front.Proxy, pins []boxapi.Pin) error {
	t.Helper()
	return refusePreviewCertificatesDeployingTo(t, state, fronting, pins, "")
}

func refusePreviewCertificatesDeployingTo(t *testing.T, state boxapi.RoutingTable, fronting front.Proxy, pins []boxapi.Pin, destination string) error {
	t.Helper()
	state.Grace = 30 * time.Second
	table := string(mustWrite(t, state))
	b := newBench(nil)
	b.answer = servesProxy(b, &table)
	h := New(ssh.NewBox(b.dial, nil), "shop", Keys{}, pins, fronting)
	return h.RefusePerHostnamePreviewCertificates(context.Background(), destination)
}

func newTraefikFront(previewResolver string) front.Proxy {
	return front.Proxy{Traefik: &front.Traefik{Directory: "/etc/traefik/dynamic", Resolver: "le", PreviewResolver: previewResolver}}
}

func TestAPreviewWhoseHostnamesEachGetAPublicCertificateIsRefusedNamingEveryWayOut(t *testing.T) {
	t.Parallel()

	for name, front := range map[string]front.Proxy{
		"ocel's own proxy":           {},
		"a Traefik with no wildcard": newTraefikFront(""),
		"your Caddy":                 {Caddy: &front.Caddy{Directory: "/etc/caddy/ocel", Config: "/etc/caddy/Caddyfile"}},
	} {
		err := refusePreviewCertificates(t, boxapi.RoutingTable{PreviewBase: publicPreviewBase}, front, nil)
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

	err := refusePreviewCertificates(t, boxapi.RoutingTable{PreviewBase: publicPreviewBase}, newTraefikFront(""), nil)
	if err == nil || !strings.Contains(err.Error(), "previewResolver") {
		t.Errorf("the refusal reads %v, want it to name previewResolver", err)
	}
}

func TestAPreviewServedUnderOneWildcardCertificateIsNotRefused(t *testing.T) {
	t.Parallel()

	origin := boxapi.CertificatePair{Certificate: "ORIGIN", Key: "KEY"}
	for name, bench := range map[string]struct {
		state boxapi.RoutingTable
		front front.Proxy
		pins  []boxapi.Pin
	}{
		"a Traefik with a preview resolver": {state: boxapi.RoutingTable{PreviewBase: publicPreviewBase}, front: newTraefikFront("dns")},
		"a proxy you route yourself":        {state: boxapi.RoutingTable{PreviewBase: publicPreviewBase}, front: front.Proxy{Manual: &front.Manual{Port: 8480}}},
		"a pinned wildcard":                 {state: boxapi.RoutingTable{PreviewBase: publicPreviewBase}, pins: []boxapi.Pin{{Hostname: "*." + publicPreviewBase, Path: boxapi.PinsDir + "/preview"}}},
		"an edge's origin certificate": {state: boxapi.RoutingTable{PreviewBase: publicPreviewBase, Shields: []boxapi.Shield{{
			Hostname: "*." + publicPreviewBase, Owner: "ocel-preview-entry", ClientCAs: []string{"zone"}, OriginCertificate: origin,
		}}}},
		"a base no public authority issues for": {state: boxapi.RoutingTable{PreviewBase: "preview.ocel.home.arpa"}},
		"no preview base on the box":            {state: boxapi.RoutingTable{}},
	} {
		if err := refusePreviewCertificates(t, bench.state, bench.front, bench.pins); err != nil {
			t.Errorf("%s: %v, want the preview let through: no preview hostname gets a publicly logged certificate of its own", name, err)
		}
	}
}

func TestAnEdgeThatShieldsThePreviewsWithoutAnOriginCertificateStillLeavesEachHostnameItsOwn(t *testing.T) {
	t.Parallel()

	err := refusePreviewCertificates(t, boxapi.RoutingTable{PreviewBase: publicPreviewBase, Shields: []boxapi.Shield{{
		Hostname: "*." + publicPreviewBase, Owner: "ocel-preview-entry", ClientCAs: []string{"zone"},
	}}}, front.Proxy{}, nil)
	if err == nil {
		t.Error("a shield with no origin certificate let the preview through, and the box still orders a public certificate per hostname behind it")
	}
}

func TestTheFirstPreviewOnABoxIsRefusedByTheBaseItDeploysTo(t *testing.T) {
	t.Parallel()

	err := refusePreviewCertificatesDeployingTo(t, boxapi.RoutingTable{}, front.Proxy{}, nil, publicPreviewBase)
	if err == nil || !strings.Contains(err.Error(), publicPreviewBase) {
		t.Errorf("the first preview on an empty box was let through with %v, want it refused by %s, the base it installs", err, publicPreviewBase)
	}
}
