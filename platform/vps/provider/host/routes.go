package host

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
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

type claimKey struct {
	Owner   string
	Pointer string
	App     string
}

type surfaceKey struct {
	Owner   string
	Pointer string
}

func (c HostClaim) key() claimKey {
	return claimKey{Owner: c.Owner, Pointer: c.Pointer, App: c.App}
}

func (r AppRoute) surface() surfaceKey {
	return surfaceKey{Owner: r.Owner, Pointer: r.Pointer}
}

func (r AppRoute) app() bool { return r.App != switchboard.StoreLabel }

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
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the preview entry on this box has no base domain")
	}
	if err := switchboard.PreviewBaseUsable(base); err != nil {
		return providerkit.Refuse(providerkit.CodeInvalid, "%v", err)
	}
	return nil
}

func Claiming(claims []HostClaim, taken HostClaim) ([]HostClaim, error) {
	if at := slices.IndexFunc(claims, func(claim HostClaim) bool { return claim.Hostname == taken.Hostname }); at >= 0 && claims[at].Owner != taken.Owner {
		return nil, providerkit.Refuse(providerkit.CodeBusy,
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

func RenderProxyConfig(state RoutingTable) ([]byte, error) {
	if err := validTable(state); err != nil {
		return nil, err
	}
	rendered, err := caddy.Render(admission(state))
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeInvalid, "%v", err)
	}
	return rendered, nil
}

func admission(state RoutingTable) proxy.Admission {
	entries := make([]proxy.Entry, 0, len(state.Claims)+1)
	for _, claim := range state.Claims {
		entries = append(entries, proxy.Entry{Hostname: claim.Hostname, Pin: Covering(state.Pins, claim.Hostname)})
	}
	if state.Connector != "" {
		entries = append(entries, proxy.Entry{Hostname: state.Connector, Pin: Covering(state.Pins, state.Connector)})
	}
	return proxy.Admission{Entries: entries, PreviewBase: state.PreviewBase, Upstream: SwitchboardAddress}
}

func validTable(state RoutingTable) error {
	claimed := map[claimKey][]string{}
	for _, claim := range slices.SortedFunc(slices.Values(state.Claims), byClaimed) {
		if err := validClaim(claim); err != nil {
			return err
		}
		claimed[claim.key()] = append(claimed[claim.key()], claim.Hostname)
	}
	standing := slices.SortedFunc(slices.Values(state.Routes), byKey)
	running := map[surfaceKey][]string{}
	for _, route := range standing {
		if err := validRoute(route); err != nil {
			return err
		}
		if route.app() && !slices.Contains(running[route.surface()], route.App) {
			running[route.surface()] = append(running[route.surface()], route.App)
		}
	}
	for _, under := range slices.SortedFunc(maps.Keys(running), bySurface) {
		wide := claimed[claimKey{Owner: under.Owner, Pointer: under.Pointer}]
		if len(wide) == 0 || len(running[under]) == 1 {
			continue
		}
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s claims %s under %s, which runs %s\nDeclare domains.production on the app that serves it, not the project",
			under.Owner, strings.Join(wide, ", "), under.Pointer, strings.Join(running[under], " and "))
	}
	answering := map[string]AppRoute{}
	for _, route := range standing {
		hostnames := claimed[claimKey{Owner: route.Owner, Pointer: route.Pointer, App: route.App}]
		if route.app() && len(running[route.surface()]) == 1 {
			hostnames = append(slices.Clone(hostnames), claimed[claimKey{Owner: route.Owner, Pointer: route.Pointer}]...)
		}
		slices.Sort(hostnames)
		for _, hostname := range hostnames {
			named := strings.ToLower(hostname)
			if held, taken := answering[named]; taken {
				return providerkit.Refuse(providerkit.CodeInvalid,
					"%s would be forwarded by both %s and %s",
					hostname, held.identity(), route.identity())
			}
			answering[named] = route
		}
	}
	if state.PreviewBase != "" {
		if err := PreviewBaseUsable(state.PreviewBase); err != nil {
			return err
		}
	}
	for _, pin := range state.Pins {
		if err := validPin(pin); err != nil {
			return err
		}
	}
	return nil
}

func pinLeaf(under string) bool {
	return under != "" && !strings.Contains(under, "/") && under != "." && under != ".."
}

func pinnedUnderProxyPins(path string) bool {
	under, beneath := strings.CutPrefix(path, caddy.PinsDir+"/")
	return beneath && pinLeaf(under)
}

func validPin(pin Pin) error {
	if pin.Hostname == "" || !pinnedUnderProxyPins(pin.Path) {
		return providerkit.Refuse(providerkit.CodeInvalid,
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

func byKey(a, b AppRoute) int {
	return strings.Compare(a.identity(), b.identity())
}

func bySurface(a, b surfaceKey) int {
	return strings.Compare(a.Owner+switchboard.ClaimSeparator+a.Pointer, b.Owner+switchboard.ClaimSeparator+b.Pointer)
}

func validRoute(route AppRoute) error {
	for _, field := range []struct{ what, named string }{
		{"surface", route.Owner}, {"pointer", route.Pointer}, {"app", route.App},
	} {
		what, named := field.what, field.named
		if named == "" || strings.Contains(named, switchboard.ClaimSeparator) {
			return providerkit.Refuse(providerkit.CodeInvalid,
				"a route on this box has an invalid %s %q",
				what, named)
		}
	}
	if _, err := switchboard.UpstreamAddress(route.Upstream); err != nil {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the route %s forwards to no upstream it can dial: %v", route.identity(), err)
	}
	return nil
}

func validClaim(claim HostClaim) error {
	switch {
	case claim.Hostname == "" || claim.Owner == "" || claim.Pointer == "":
		return providerkit.Refuse(providerkit.CodeInvalid,
			"a hostname claim on this box is incomplete: host %q, surface %q, pointer %q",
			claim.Hostname, claim.Owner, claim.Pointer)
	case strings.Contains(claim.Hostname, "*"):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"a hostname claim on this box names wildcard %q",
			claim.Hostname)
	case strings.Contains(claim.Owner, switchboard.ClaimSeparator) || strings.Contains(claim.Hostname, switchboard.ClaimSeparator) ||
		strings.Contains(claim.Pointer, switchboard.ClaimSeparator) || strings.Contains(claim.App, switchboard.ClaimSeparator):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the claim of %q by %q under pointer %q and app %q contains %q",
			claim.Hostname, claim.Owner, claim.Pointer, claim.App, switchboard.ClaimSeparator)
	}
	return nil
}

func spelled(window time.Duration) string {
	return fmt.Sprintf("%ds", int(window.Round(time.Second).Seconds()))
}
