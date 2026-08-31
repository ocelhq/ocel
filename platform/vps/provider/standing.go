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
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
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

func (p *Provider) CheckStanding(ctx context.Context, req providerkit.StandingRequest) ([]providerkit.StandingCheck, error) {
	address, err := p.host.Address(ctx)
	if err != nil {
		return []providerkit.StandingCheck{{
			Subject: host.ProxyContainer,
			Verdict: providerkit.StandingFail,
			Finding: fmt.Sprintf("this box's own address could not be read, so nothing here could be judged against it: %v", err),
			Fix:     "check the machine answers over ssh, then run this again",
		}}, nil
	}
	checks := dnsVerdicts(ctx, p.lookup(), req.Hostnames, address)
	checks = append(checks, reachVerdict(ctx, p.reach(), address))
	checks = append(checks, p.adminVerdict(ctx))
	return checks, nil
}

func dnsVerdicts(ctx context.Context, look Lookup, hostnames []string, address string) []providerkit.StandingCheck {
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
	checks := make([]providerkit.StandingCheck, 0, len(asked))
	for _, named := range asked {
		checks = append(checks, dnsVerdict(ctx, look, named, address, here, unread))
	}
	return checks
}

func dnsVerdict(ctx context.Context, look Lookup, hostname, address string, here []netip.Addr, unread error) providerkit.StandingCheck {
	check := providerkit.StandingCheck{Subject: hostname}
	if unread != nil {
		check.Verdict = providerkit.StandingFail
		check.Finding = fmt.Sprintf("%s could not be read as this box's own address, so where %s points cannot be judged against it: %v", address, hostname, unread)
		return check
	}
	found, err := look(ctx, hostname)
	switch {
	case notResolved(err):
		check.Verdict = providerkit.StandingOwed
		check.Finding = fmt.Sprintf("%s does not resolve; the record pointing it at %s is owed", hostname, address)
		check.Fix = "add the record `ocel domain add` printed, then run this again — ocel writes no DNS"
		return check
	case err != nil:
		check.Verdict = providerkit.StandingFail
		check.Finding = fmt.Sprintf("%s could not be resolved from here, so nothing was learned about where it points: %v", hostname, err)
		return check
	case len(found) == 0:
		check.Verdict = providerkit.StandingOwed
		check.Finding = fmt.Sprintf("%s does not resolve; the record pointing it at %s is owed", hostname, address)
		check.Fix = "add the record `ocel domain add` printed, then run this again — ocel writes no DNS"
		return check
	case pointsHere(found, here):
		check.Verdict = providerkit.StandingPass
		check.Finding = fmt.Sprintf("%s resolves to %s, which is this box", hostname, spell(found))
		return check
	default:
		check.Verdict = providerkit.StandingFail
		check.Finding = fmt.Sprintf("%s resolves to %s, and this box is %s", hostname, spell(found), spell(here))
		check.Fix = fmt.Sprintf("point %s at %s in your zone, or ocel serves it from a machine that is not this one", hostname, spell(here))
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

func spell(addrs []netip.Addr) string {
	written := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		written = append(written, addr.Unmap().String())
	}
	return strings.Join(written, ", ")
}

func reachVerdict(ctx context.Context, dial Reach, address string) providerkit.StandingCheck {
	at := net.JoinHostPort(address, host.RenewalPort)
	check := providerkit.StandingCheck{Subject: at}
	if err := dial(ctx, at); err != nil {
		check.Verdict = providerkit.StandingFail
		check.Finding = fmt.Sprintf("nothing answered a connection to %s from this machine, and the proxy renews every certificate on this box over http-01 on port %s with no ocel code anywhere near it: %v",
			at, host.RenewalPort, err)
		check.Fix = "open port " + host.RenewalPort + " to the internet in this machine's firewall and its provider's, and check the proxy is standing"
		return check
	}
	check.Verdict = providerkit.StandingPass
	check.Finding = fmt.Sprintf("something listens on port %s, this box's firewall permits it, and a connection from this machine succeeded — that is one path in, not proof the internet reaches it",
		host.RenewalPort)
	return check
}

func (p *Provider) adminVerdict(ctx context.Context) providerkit.StandingCheck {
	check := providerkit.StandingCheck{Subject: host.ProxyContainer + " tcp " + host.AdminPort}
	held, err := p.host.ProxyListeners(ctx)
	if err != nil {
		check.Verdict = providerkit.StandingFail
		check.Finding = fmt.Sprintf("what listens inside %s could not be read, so whether the stock admin port is bound is unknown: %v", host.ProxyContainer, err)
		check.Fix = "check the proxy is running, then run this again"
		return check
	}
	if len(held) == 0 {
		check.Verdict = providerkit.StandingFail
		check.Finding = fmt.Sprintf("%s named no listening socket at all, and a proxy that is serving holds ports %s at minimum, so this is a namespace nothing was read out of rather than one with the stock admin port clean",
			host.ProxyContainer, strings.Join(host.ProxyServing(), " and "))
		check.Fix = "check the proxy is running with its own /proc mounted, then run this again"
		return check
	}
	if bound := listeners.On(held, host.AdminPortNumber); len(bound) > 0 {
		check.Verdict = providerkit.StandingFail
		check.Finding = fmt.Sprintf("%s listens on %s inside %s, which is the stock unauthenticated admin api: every container on the %s network holds arbitrary replacement of this box's serving configuration",
			strings.Join(listeners.Lines(bound), ", "), host.AdminPort, host.ProxyContainer, host.ProxyNetwork)
		check.Fix = "run `ocel bootstrap production` to put back the configuration that binds the admin endpoint to " + host.ProxyAdminSocket + " and nothing else"
		return check
	}
	check.Verdict = providerkit.StandingPass
	check.Finding = fmt.Sprintf("nothing listens on tcp %s inside %s; the admin endpoint is reached over %s alone",
		host.AdminPort, host.ProxyContainer, host.ProxyAdminSocket)
	return check
}

var _ providerkit.StandingChecker = (*Provider)(nil)
