package host

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	DeployWindow = 60 * time.Second
	DrainWindow  = 30 * time.Second
)

type RouteKey struct {
	Owner   string `json:"owner"`
	Pointer string `json:"pointer"`
	App     string `json:"app"`
}

func (k RouteKey) identity() string {
	return switchboard.RouteIdentity + strings.Join([]string{k.Owner, k.Pointer, k.App}, switchboard.ClaimSeparator)
}

type AppRoute struct {
	RouteKey
	Upstream string `json:"upstream"`
}

type HostClaim live.Claimed

type Shield struct {
	Hostname           string   `json:"hostname"`
	Owner              string   `json:"owner"`
	ClientCertificates []string `json:"clientCertificates,omitempty"`
}

func Shielding(shields []Shield, taken Shield) []Shield {
	taken.ClientCertificates = slices.Compact(slices.Sorted(slices.Values(taken.ClientCertificates)))
	kept := make([]Shield, 0, len(shields)+1)
	for _, shield := range shields {
		if shield.Hostname == taken.Hostname {
			continue
		}
		if slices.ContainsFunc(shield.ClientCertificates, func(certificate string) bool { return slices.Contains(taken.ClientCertificates, certificate) }) {
			shield.ClientCertificates = slices.Clone(taken.ClientCertificates)
		}
		kept = append(kept, shield)
	}
	return append(kept, taken)
}

func Unshielding(shields []Shield, dropped func(Shield) bool) []Shield {
	return slices.DeleteFunc(slices.Clone(shields), dropped)
}

type Pin struct {
	Hostname string `json:"hostname"`
	Path     string `json:"path"`
}

func (p Pin) Covers(hostname string) bool {
	return (certs.Leaf{Domains: []string{p.Hostname}}).Covers(hostname)
}

func Covering(pins []Pin, hostname string) string {
	for _, exact := range []bool{true, false} {
		at := slices.IndexFunc(pins, func(pin Pin) bool {
			return exact == (pin.Hostname == hostname) && pin.Covers(hostname) && validPin(pin) == nil
		})
		if at >= 0 {
			return pins[at].Path
		}
	}
	return ""
}

func PreviewBaseUsable(base string) error {
	if base == "" {
		return refusal.Refuse(refusal.CodeInvalid,
			"the preview entry on this box has no base domain")
	}
	if err := switchboard.PreviewBaseUsable(base); err != nil {
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

func proxySpec(state RoutingTable) proxy.Spec {
	pins := make([]proxy.Pin, 0, len(state.Pins))
	for _, pin := range state.Pins {
		pins = append(pins, proxy.Pin(pin))
	}
	shields := make([]proxy.Shield, 0, len(state.Shields))
	for _, shield := range state.Shields {
		shields = append(shields, proxy.Shield{Hostname: shield.Hostname, ClientCertificates: shield.ClientCertificates})
	}
	return proxy.Spec{
		Pins:        pins,
		Shields:     shields,
		Hostnames:   state.hostnames(),
		PreviewBase: state.PreviewBase,
		Upstream:    SwitchboardUpstream,
		Router:      switchboard.RouterKind,
		Permission:  SwitchboardPermission,
	}
}

func validTable(state RoutingTable) error {
	written, err := WriteRoutingTable(state)
	if err != nil {
		return err
	}
	if _, err := switchboard.Read(written); err != nil {
		return refusal.Refuse(refusal.CodeInvalid, "%v", err)
	}
	for _, pin := range state.Pins {
		if err := validPin(pin); err != nil {
			return err
		}
	}
	return nil
}

func validPin(pin Pin) error {
	if _, pinned := caddy.Pinned(pin.Path); pin.Hostname == "" || !pinned {
		return refusal.Refuse(refusal.CodeInvalid,
			"pinned certificate for %q is at %q, not under %s/",
			pin.Hostname, pin.Path, caddy.PinsDir)
	}
	return nil
}

func byClaimed(a, b HostClaim) int {
	return strings.Compare(a.Hostname, b.Hostname)
}

func byPinned(a, b Pin) int {
	return cmp.Or(strings.Compare(a.Hostname, b.Hostname), strings.Compare(a.Path, b.Path))
}

func byShielded(a, b Shield) int {
	return cmp.Or(strings.Compare(a.Hostname, b.Hostname), strings.Compare(a.Owner, b.Owner))
}

func byKey(a, b AppRoute) int {
	return strings.Compare(a.identity(), b.identity())
}

func spelled(window time.Duration) string {
	return fmt.Sprintf("%ds", int(window.Round(time.Second).Seconds()))
}
