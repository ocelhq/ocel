package traefik

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const originRenewal = "ocel renews the origin certificate the edge in front issues it, and your Traefik serves it from " + FileName

var bootstrapFix = "run `" + provider.BootstrapCommand(environment.TierProduction) + "` to recreate the switchboard where your Traefik reaches it"

func (t Traefik) inspect(ctx context.Context) (proxy.Checks, error) {
	spec, err := t.Box.ReadSpec(ctx)
	if err != nil {
		return nil, err
	}
	checks, err := t.switchboardChecks(ctx)
	if err != nil {
		return nil, err
	}
	placed, err := t.placedCheck(ctx, spec)
	if err != nil {
		return nil, err
	}
	checks = append(checks, placed)
	diagnosis := sync.OnceValue(func() string { return t.diagnose(ctx) })
	for _, hostname := range spec.Hostnames {
		if _, shielded := spec.ShieldOf(hostname); shielded {
			check, err := t.shieldCheck(ctx, hostname)
			if err != nil {
				return nil, err
			}
			checks = append(checks, check)
			continue
		}
		routing, err := t.routingCheck(ctx, hostname, diagnosis)
		if err != nil {
			return nil, err
		}
		checks = append(checks, routing)
		if routing.Verdict != provider.HostPass {
			continue
		}
		certificate, err := t.certificate(ctx, spec, hostname)
		if err != nil {
			return nil, err
		}
		if certificate.Trouble != nil {
			checks = append(checks, provider.HostCheck{
				Subject: hostname + " certificate",
				Verdict: provider.HostNeedsAction,
				Finding: certificate.Trouble.Error(),
				Fix:     "point " + hostname + "'s DNS at this box, then restart your Traefik or change any route it serves so it orders again",
			})
		}
	}
	outranked, err := t.outranked(ctx, spec.Hostnames)
	if err != nil {
		return nil, err
	}
	return append(checks, outranked...), nil
}

func (t Traefik) switchboardChecks(ctx context.Context) (proxy.Checks, error) {
	said, err := t.Box.Ran(ctx, "read what "+switchboard.Name+" mounts and which networks it is on", switchboardFacts())
	if err != nil {
		return nil, err
	}
	var mounts, networks []string
	for _, fact := range strings.Fields(said) {
		if mount, ok := strings.CutPrefix(fact, mountFact); ok {
			mounts = append(mounts, filepath.Clean(mount))
		}
		if network, ok := strings.CutPrefix(fact, networkFact); ok {
			networks = append(networks, network)
		}
	}
	dir := t.directory()
	mounted := provider.HostCheck{Subject: switchboard.Name + " mounts " + dir, Verdict: provider.HostPass,
		Finding: switchboard.Name + " has " + dir + " mounted to place ocel.yml in"}
	if !slices.Contains(mounts, dir) {
		mounted.Verdict, mounted.Fix = provider.HostFail, bootstrapFix
		mounted.Finding = switchboard.Name + " does not have " + dir + " mounted, so ocel cannot place its routes where your Traefik reads them"
	}
	checks := proxy.Checks{mounted}
	if t.Network == "" {
		return checks, nil
	}
	joined := provider.HostCheck{Subject: switchboard.Name + " on " + t.Network, Verdict: provider.HostPass,
		Finding: switchboard.Name + " is on " + t.Network + ", where your Traefik reaches it by name"}
	if !slices.Contains(networks, t.Network) {
		joined.Verdict, joined.Fix = provider.HostFail, bootstrapFix
		joined.Finding = switchboard.Name + " is not on " + t.Network + ", so your Traefik cannot reach it by name"
	}
	return append(checks, joined), nil
}

func (t Traefik) placedCheck(ctx context.Context, spec proxy.Spec) (provider.HostCheck, error) {
	check := provider.HostCheck{Subject: t.file(), Verdict: provider.HostFail,
		Fix: "run a deploy, or `" + provider.BootstrapCommand(environment.TierProduction) + "`, to place it again"}
	rendered, err := t.render(spec)
	if err != nil {
		return check, err
	}
	placed, err := t.Box.PlacedSum(ctx, t.file())
	if err != nil {
		return check, err
	}
	summed := sha256.Sum256(rendered)
	switch placed {
	case "":
		check.Finding = t.file() + " is gone, so your Traefik routes none of ocel's hostnames"
	case hex.EncodeToString(summed[:]):
		check.Verdict, check.Fix = provider.HostPass, ""
		check.Finding = t.file() + " is what ocel renders from the routing table"
	default:
		check.Finding = t.file() + " differs from what ocel renders from the routing table: something rewrote it, as a host tool's editor does"
	}
	return check, nil
}

func (t Traefik) routingCheck(ctx context.Context, hostname string, diagnosis func() string) (provider.HostCheck, error) {
	routed, reason, err := t.probeRoute(ctx, hostname)
	if err != nil {
		return provider.HostCheck{}, err
	}
	if routed {
		return provider.HostCheck{Subject: hostname, Verdict: provider.HostPass,
			Finding: "your Traefik routes " + hostname + " to ocel's switchboard"}, nil
	}
	return provider.HostCheck{Subject: hostname, Verdict: provider.HostFail, Finding: reason + diagnosis(),
		Fix: "check your Traefik reads " + t.directory() + " and runs its " + t.HTTPS + " entry point on 443"}, nil
}

func (t Traefik) shieldCheck(ctx context.Context, hostname string) (provider.HostCheck, error) {
	refused, said, err := t.probeShield(ctx, hostname)
	if err != nil {
		return provider.HostCheck{}, err
	}
	if !refused {
		return provider.HostCheck{Subject: hostname, Verdict: provider.HostFail, Finding: said,
			Fix: "check your Traefik reads " + t.directory() + " and runs its " + t.HTTPS + " entry point on 443"}, nil
	}
	return provider.HostCheck{Subject: hostname, Verdict: provider.HostPass,
		Finding: fmt.Sprintf("your Traefik refuses %s to a client that presents no certificate (%s)", hostname, said)}, nil
}

func (t Traefik) outranked(ctx context.Context, hostnames []string) (proxy.Checks, error) {
	routers, unread, err := t.routers(ctx)
	if err != nil {
		return nil, err
	}
	var checks proxy.Checks
	for _, file := range unread {
		checks = append(checks, provider.HostCheck{
			Subject: "routers in " + file.path,
			Verdict: provider.HostFail,
			Finding: file.said() + ", so it cannot tell whether one of them takes a hostname ocel serves",
			Fix:     "fix or remove " + file.path,
		})
	}
	for _, each := range collisions(hostnames, routers) {
		checks = append(checks, provider.HostCheck{
			Subject: "router " + each.router.name + " in " + each.router.where,
			Verdict: provider.HostFail,
			Finding: each.said() + ", and ocel's router outranks it, so it no longer serves",
			Fix:     "remove router " + each.router.name + " from " + each.router.where + ", or run `ocel domain rm " + each.hostname + "` to hand the hostname back to it",
		})
	}
	return checks, nil
}

func (t Traefik) certificate(ctx context.Context, spec proxy.Spec, hostname string) (proxy.Certificate, error) {
	if shield, shielded := spec.ShieldOf(hostname); shielded && shield.OriginCertificate.Certificate != "" {
		return proxy.Certificate{Renewal: originRenewal}, nil
	}
	certificate := proxy.Certificate{Renewal: fmt.Sprintf("your Traefik renews it through %s, for as long as its acme.json holds it",
		t.resolverOf(hostname, spec.PreviewBase))}
	block, err := t.Box.ReadLeaf(ctx, hostname)
	if err != nil {
		return certificate, err
	}
	if len(block) > 0 {
		leaf, err := certs.Parse("the certificate your Traefik serves for "+hostname, block)
		if err != nil {
			return certificate, err
		}
		if leaf.Covers(hostname) && !leaf.Expired(time.Now()) {
			return certificate, nil
		}
	}
	certificate.Trouble = refusal.Refuse(refusal.CodeNotReady,
		"your Traefik holds no certificate for %s, and it orders one again only on its next configuration change, with no backoff",
		hostname)
	return certificate, nil
}

func (t Traefik) resolverOf(hostname, base string) string {
	if t.PreviewResolver != "" && base != "" &&
		(hostname == edge.ProbeHostname(edge.PreviewWildcard(base)) || previewed(hostname, base)) {
		return t.PreviewResolver
	}
	return t.Resolver
}
