package vps_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func byHand(machine host.Conn) *vps.Provider {
	return vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}, Proxy: &vps.Proxy{Manual: &vps.Manual{}}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
}

func TestACertificateYourProxyServesIsRenewedByYourProxy(t *testing.T) {
	t.Parallel()

	served := selfSigned(t, []string{"shop.example.com"}, 60*24*time.Hour)
	machine := &box{leaf: string(served)}
	p := byHand(machine)
	cert := certificateFor(t, p, "shop.example.com")
	health, err := p.Certificates().Inspect(context.Background(), edge.None, "shop.example.com", cert)
	if err != nil {
		t.Fatalf("InspectCertificate() = %v", err)
	}
	if health.Renewal != certs.AdoptedRenewal || !health.Issued || !health.Covers {
		t.Errorf("InspectCertificate() = %+v, want the leaf your proxy serves read as issued and renewed by it", health)
	}
	for _, command := range machine.commands() {
		if strings.Contains(command, "docker") {
			t.Errorf("reading what your proxy serves ran %q, want the handshake alone asked", command)
		}
	}
}

func TestACertificateYourCaddyServesSaysYourCaddyRenewsIt(t *testing.T) {
	t.Parallel()

	served := selfSigned(t, []string{"shop.example.com"}, 60*24*time.Hour)
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}, Proxy: &vps.Proxy{Caddy: &vps.Caddy{Directory: "/etc/caddy/ocel.d"}}},
		func(context.Context) (host.Conn, error) { return &box{leaf: string(served)}, nil },
	)
	health, err := p.Certificates().Inspect(context.Background(), edge.None, "shop.example.com", certificateFor(t, p, "shop.example.com"))
	if err != nil {
		t.Fatalf("InspectCertificate() = %v", err)
	}
	if health.Renewal != caddyfile.Renewal {
		t.Errorf("InspectCertificate() = %+v, want renewal %q, from the proxy that fronts the box", health, caddyfile.Renewal)
	}
}

func TestAHostnameOnABoxYourProxyFrontsIsProbedFromTheBoxItself(t *testing.T) {
	t.Parallel()

	machine := routedByHand(map[string]answer{"'probe' 'shop.example.com'": {stdout: "switchboard\n"}})
	p := byHand(machine)
	p.System = resolvesNothing{}
	served, err := p.ServingRouter(context.Background(), "shop.example.com")
	if err != nil || served != switchboard.RouterKind {
		t.Fatalf("Serving() = %q, %v, want the box's own probe to answer switchboard", served, err)
	}
	if !slices.ContainsFunc(machine.ran, func(command string) bool { return strings.Contains(command, "'probe' 'shop.example.com'") }) {
		t.Errorf("the box was asked %q, want its switchboard helper to probe 127.0.0.1:443 for the hostname: your proxy answers there before any record points at it", machine.ran)
	}
}

func TestTheCheckOfABoxYourProxyFrontsAsksNothingOfPort80(t *testing.T) {
	t.Parallel()

	machine := routedByHand(nil)
	p := byHand(machine)
	p.Resolving(stubResolver(map[string][]string{"box.invalid": {boxAddress}}))
	var reached []string
	p.Reaching(func(_ context.Context, address string) error {
		reached = append(reached, address)
		return nil
	})
	checks, err := p.CheckHost(context.Background(), provider.HostCheckRequest{Tier: environment.TierProduction})
	if err != nil {
		t.Fatalf("CheckHost() = %v", err)
	}
	if len(reached) != 0 {
		t.Errorf("CheckHost() dialled %v, want nothing: port 80 matters to ocel's own proxy renewing over http-01, and yours renews as it chooses", reached)
	}
	if !slices.ContainsFunc(checks, func(check provider.HostCheck) bool { return check.Subject == "tcp 443" }) {
		t.Errorf("CheckHost() = %+v, want your proxy's port 443 checked", checks)
	}
}
