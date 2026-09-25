package vps

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

const (
	reachTimeout  = 10 * time.Second
	lookupTimeout = 10 * time.Second
)

type Lookup func(ctx context.Context, hostname string) ([]netip.Addr, error)

type Reach func(ctx context.Context, address string) error

func systemLookup(ctx context.Context, hostname string) ([]netip.Addr, error) {
	asking, stop := context.WithTimeout(ctx, lookupTimeout)
	defer stop()
	return net.DefaultResolver.LookupNetIP(asking, "ip", hostname)
}

func systemReach(ctx context.Context, address string) error {
	held, err := (&net.Dialer{Timeout: reachTimeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return held.Close()
}

func (p *Provider) lookup() Lookup {
	if p.resolve != nil {
		return p.resolve
	}
	return systemLookup
}

func (p *Provider) reach() Reach {
	if p.reaches != nil {
		return p.reaches
	}
	return systemReach
}

func (p *Provider) CheckHost(ctx context.Context, req providerkit.HostCheckRequest) ([]providerkit.HostCheck, error) {
	address, err := p.host.Address(ctx)
	if err != nil {
		return []providerkit.HostCheck{{
			Subject: caddy.Container,
			Verdict: providerkit.HostFail,
			Finding: fmt.Sprintf("read this box's address: %v", err),
			Fix:     "check the machine answers over ssh",
		}}, nil
	}
	checks := dnsVerdicts(ctx, p.lookup(), req.Hostnames, address)
	if p.host.FrontProxy().Guarantees().OwnsPorts {
		checks = append(checks, reachVerdict(ctx, p.reach(), address))
	}
	front, err := p.host.FrontProxy().Inspect(ctx)
	if err != nil {
		return nil, err
	}
	checks = append(checks, front...)
	return append(checks, p.host.CheckSwitchboard(ctx, req.Class)), nil
}

func dnsVerdicts(ctx context.Context, look Lookup, hostnames []string, address string) []providerkit.HostCheck {
	var asked []string
	for _, hostname := range hostnames {
		named := edge.ProbeHostname(hostname)
		if named == "" || slices.Contains(asked, named) {
			continue
		}
		asked = append(asked, named)
	}
	if len(asked) == 0 {
		return nil
	}
	here, unread := look(ctx, address)
	checks := make([]providerkit.HostCheck, 0, len(asked))
	for _, named := range asked {
		checks = append(checks, dnsVerdict(ctx, look, named, address, here, unread))
	}
	return checks
}

func dnsVerdict(ctx context.Context, look Lookup, hostname, address string, here []netip.Addr, unread error) providerkit.HostCheck {
	check := providerkit.HostCheck{Subject: hostname}
	if unread != nil {
		check.Verdict = providerkit.HostFail
		check.Finding = fmt.Sprintf("resolve this box's address %s: %v", address, unread)
		return check
	}
	found, err := look(ctx, hostname)
	switch {
	case notResolved(err):
		check.Verdict = providerkit.HostOwed
		check.Finding = fmt.Sprintf("%s does not resolve; the record pointing it at %s is owed", hostname, address)
		check.Fix = "add the record `ocel domain add` printed"
		return check
	case err != nil:
		check.Verdict = providerkit.HostFail
		check.Finding = fmt.Sprintf("resolve %s: %v", hostname, err)
		return check
	case len(found) == 0:
		check.Verdict = providerkit.HostOwed
		check.Finding = fmt.Sprintf("%s does not resolve; the record pointing it at %s is owed", hostname, address)
		check.Fix = "add the record `ocel domain add` printed"
		return check
	case pointsHere(found, here):
		check.Verdict = providerkit.HostPass
		check.Finding = fmt.Sprintf("%s resolves to %s, which is this box", hostname, spell(found))
		return check
	case loopbackOnly(found):
		check.Verdict = providerkit.HostOwed
		check.Finding = fmt.Sprintf("%s resolves to loopback %s; the record pointing it at %s is owed",
			hostname, spell(found), address)
		check.Fix = "add the record `ocel domain add` printed"
		return check
	default:
		check.Verdict = providerkit.HostFail
		check.Finding = fmt.Sprintf("%s resolves to %s, and this box is %s", hostname, spell(found), spell(here))
		check.Fix = fmt.Sprintf("point %s at %s in your zone", hostname, spell(here))
		return check
	}
}

func notResolved(err error) bool {
	var refused *net.DNSError
	return errors.As(err, &refused) && refused.IsNotFound
}

func pointsHere(found, here []netip.Addr) bool {
	for _, addr := range found {
		if slices.ContainsFunc(here, func(mine netip.Addr) bool { return mine.Unmap() == addr.Unmap() }) {
			return true
		}
	}
	return false
}

func loopbackOnly(found []netip.Addr) bool {
	return !slices.ContainsFunc(found, func(addr netip.Addr) bool { return !addr.Unmap().IsLoopback() })
}

func spell(addrs []netip.Addr) string {
	written := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		written = append(written, addr.Unmap().String())
	}
	return strings.Join(written, ", ")
}

func reachVerdict(ctx context.Context, dial Reach, address string) providerkit.HostCheck {
	at := net.JoinHostPort(address, caddy.HTTPPort)
	check := providerkit.HostCheck{Subject: at}
	if err := dial(ctx, at); err != nil {
		check.Verdict = providerkit.HostFail
		check.Finding = fmt.Sprintf("%s is unreachable, so the proxy cannot renew certificates over http-01: %v",
			at, err)
		check.Fix = "open port " + caddy.HTTPPort + " in your provider's firewall or security group; docker's iptables rules bypass ufw"
		return check
	}
	check.Verdict = providerkit.HostPass
	check.Finding = fmt.Sprintf("port %s answers from this machine (not proof the internet reaches it)",
		caddy.HTTPPort)
	return check
}
