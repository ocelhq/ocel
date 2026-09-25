package vps_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
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
	health, err := p.Certificates().Inspect(context.Background(), boxedge.Kind, "shop.example.com", cert)
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

func TestAHostnameOnABoxYourProxyFrontsIsProbedFromTheBoxItself(t *testing.T) {
	t.Parallel()

	machine := routedByHand(map[string]answer{"'probe' 'shop.example.com'": {stdout: "box\n"}})
	p := byHand(machine)
	p.System = resolvesNothing{}
	served, err := p.ServingEdge(context.Background(), boxedge.Kind, "shop.example.com")
	if err != nil || served != boxedge.Kind {
		t.Fatalf("Serving() = %q, %v, want the box's own probe to answer box", served, err)
	}
	if !slices.ContainsFunc(machine.ran, func(command string) bool { return strings.Contains(command, "'probe' 'shop.example.com'") }) {
		t.Errorf("the box was asked %q, want its switchboard helper to probe 127.0.0.1:443 for the hostname: your proxy answers there before any record points at it", machine.ran)
	}
}

func TestTheStandingOfABoxYourProxyFrontsAsksNothingOfPort80(t *testing.T) {
	t.Parallel()

	machine := routedByHand(nil)
	p := byHand(machine)
	p.Resolving(stubResolver(map[string][]string{"box.invalid": {boxAddress}}))
	var reached []string
	p.Reaching(func(_ context.Context, address string) error {
		reached = append(reached, address)
		return nil
	})
	checks, err := p.CheckHost(context.Background(), providerkit.HostCheckRequest{Class: providerkit.ClassProduction})
	if err != nil {
		t.Fatalf("CheckHost() = %v", err)
	}
	if len(reached) != 0 {
		t.Errorf("CheckHost() dialled %v, want nothing: port 80 matters to ocel's own proxy renewing over http-01, and yours renews as it chooses", reached)
	}
	if !slices.ContainsFunc(checks, func(check providerkit.HostCheck) bool { return check.Subject == "tcp 443" }) {
		t.Errorf("CheckHost() = %+v, want your proxy's hold on 443 checked", checks)
	}
}
