package host

import (
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

const (
	DeployWindow = 60 * time.Second
	DrainWindow  = 30 * time.Second
)

type RouteKey = boxapi.RouteKey

type AppRoute = boxapi.Route

type HostClaim = boxapi.Claim

type Shield = boxapi.Shield

func MergeShield(shields []Shield, merged Shield) []Shield {
	merged.ClientCAs = slices.Compact(slices.Sorted(slices.Values(merged.ClientCAs)))
	kept := make([]Shield, 0, len(shields)+1)
	for _, shield := range shields {
		if shield.Hostname == merged.Hostname {
			if merged.OriginCertificate.Certificate == "" && shield.Owner == merged.Owner {
				merged.OriginCertificate = shield.OriginCertificate
			}
			continue
		}
		if slices.ContainsFunc(shield.ClientCAs, func(certificate string) bool { return slices.Contains(merged.ClientCAs, certificate) }) {
			shield.ClientCAs = slices.Compact(slices.Sorted(slices.Values(slices.Concat(shield.ClientCAs, merged.ClientCAs))))
		}
		kept = append(kept, shield)
	}
	return append(kept, merged)
}

func DropShields(shields []Shield, dropped func(Shield) bool) []Shield {
	return slices.DeleteFunc(slices.Clone(shields), dropped)
}

type OwnedGate = boxapi.OwnedGate

type Pin = boxapi.Pin

func pinCovers(pin Pin, hostname string) bool {
	return (certs.Leaf{Domains: []string{pin.Hostname}}).Covers(hostname)
}

func Covering(pins []Pin, hostname string) string {
	for _, exact := range []bool{true, false} {
		at := slices.IndexFunc(pins, func(pin Pin) bool {
			return exact == (pin.Hostname == hostname) && pinCovers(pin, hostname) && validPin(pin) == nil
		})
		if at >= 0 {
			return pins[at].Path
		}
	}
	return ""
}

func RefuseInvalidPreviewBase(base string) error {
	if base == "" {
		return refusal.Refuse(refusal.CodeInvalid,
			"the preview entry on this box has no base domain")
	}
	if err := edge.RefuseUnusablePreviewBase(base); err != nil {
		return refusal.Refuse(refusal.CodeInvalid, "%v", err)
	}
	return nil
}

func Claiming(claims []HostClaim, taken HostClaim) ([]HostClaim, error) {
	if at := slices.IndexFunc(claims, func(claim HostClaim) bool { return claim.Hostname == taken.Hostname }); at >= 0 && claims[at].Owner != taken.Owner {
		return nil, refusal.Refuse(refusal.CodeBusy,
			"%s is claimed on this box by %s\nUnbind it there before %s binds it",
			taken.Hostname, claims[at].Owner, taken.Owner)
	}
	kept := slices.DeleteFunc(slices.Clone(claims), func(claim HostClaim) bool { return claim.Hostname == taken.Hostname })
	return append(kept, taken), nil
}

func Disclaiming(claims []HostClaim, dropped func(HostClaim) bool) []HostClaim {
	return slices.DeleteFunc(slices.Clone(claims), dropped)
}

func Routing(routes []AppRoute, taken AppRoute) []AppRoute {
	return append(Unrouting(routes, func(route AppRoute) bool { return route.RouteKey == taken.RouteKey }), taken)
}

func Unrouting(routes []AppRoute, dropped func(AppRoute) bool) []AppRoute {
	return slices.DeleteFunc(slices.Clone(routes), dropped)
}

func RenderProxyConfig(front proxy.Proxy, state RoutingTable) ([]byte, error) {
	if err := validTable(state); err != nil {
		return nil, err
	}
	rendered, err := front.Render(proxySpec(state))
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid, "%v", err)
	}
	return rendered, nil
}

func RenderOriginFiles(front proxy.Proxy, state RoutingTable) ([]proxy.OriginFile, error) {
	files, err := front.OriginFiles(proxySpec(state))
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid, "%v", err)
	}
	return files, nil
}

func proxySpec(state RoutingTable) proxy.Spec {
	pins := make([]proxy.Pin, 0, len(state.Pins))
	for _, pin := range state.Pins {
		pins = append(pins, proxy.Pin(pin))
	}
	shields := make([]proxy.Shield, 0, len(state.Shields))
	for _, shield := range state.Shields {
		shields = append(shields, proxy.Shield{
			Hostname: shield.Hostname, ClientCAs: shield.ClientCAs, OriginCertificate: shield.OriginCertificate,
		})
	}
	return proxy.Spec{
		Pins:        pins,
		Shields:     shields,
		Hostnames:   state.Hostnames(),
		PreviewBase: state.PreviewBase,
		Upstream:    SwitchboardUpstream,
		Router:      boxapi.SwitchboardRouter,
		Permission:  SwitchboardPermission,
	}
}

func validTable(state RoutingTable) error {
	if err := state.Validate(); err != nil {
		return refusal.Refuse(refusal.CodeInvalid, "%v", err)
	}
	return nil
}

func validPin(pin Pin) error {
	if _, pinned := boxapi.Pinned(pin.Path); pin.Hostname == "" || !pinned {
		return refusal.Refuse(refusal.CodeInvalid,
			"pinned certificate for %q is at %q, not under %s/",
			pin.Hostname, pin.Path, boxapi.PinsDir)
	}
	return nil
}
