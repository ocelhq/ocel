package host

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/caddyadmin"
)

const (
	proxyServer      = "ocel"
	proxyDrainServer = "ocel_drain"
	drainListen      = "127.0.0.1:9"
	routeIdentity    = "ocel-app-"
	claimIdentity    = "ocel-host-"
	claimSeparator   = "/"
	drainIdentity    = "ocel-retiring"
	boxIdentity      = "ocel-box"
)

const (
	EdgeHeader = "X-Ocel-Edge"
	EdgeName   = "box"
)

const (
	DeployWindow = 60 * time.Second
	DrainWindow  = 30 * time.Second
)

type RouteKey struct {
	Owner   string
	Pointer string
	App     string
}

func (k RouteKey) identity() string {
	return routeIdentity + strings.Join([]string{k.Owner, k.Pointer, k.App}, claimSeparator)
}

type AppRoute struct {
	RouteKey
	Upstream string
}

type HostClaim struct {
	Hostname string
	Owner    string
}

type ProxyState struct {
	Grace    time.Duration
	Claims   []HostClaim
	Routes   []AppRoute
	Retiring string
}

func Claiming(claims []HostClaim, taken HostClaim) ([]HostClaim, error) {
	if at := slices.IndexFunc(claims, func(claim HostClaim) bool { return claim.Hostname == taken.Hostname }); at >= 0 && claims[at].Owner != taken.Owner {
		return nil, providerkit.Refuse(providerkit.CodeBusy,
			"%s is claimed on this box by %s, and one hostname answers for one surface: unbind it there before %s binds it, or this bind would take a live site off the air with nothing telling the project that lost it",
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

type caddyConfig struct {
	Admin   caddyAdmin      `json:"admin"`
	Logging json.RawMessage `json:"logging,omitempty"`
	Apps    caddyApps       `json:"apps"`
}

type caddyAdmin struct {
	Listen string `json:"listen"`
}

type caddyApps struct {
	HTTP caddyHTTP `json:"http"`
}

type caddyHTTP struct {
	GracePeriod string                 `json:"grace_period"`
	Servers     map[string]caddyServer `json:"servers"`
}

type caddyServer struct {
	Listen []string        `json:"listen"`
	Logs   json.RawMessage `json:"logs,omitempty"`
	Routes []caddyRoute    `json:"routes"`
}

type caddyRoute struct {
	Identity string         `json:"@id,omitempty"`
	Match    []caddyMatch   `json:"match,omitempty"`
	Handle   []caddyForward `json:"handle"`
}

type caddyMatch struct {
	Host []string `json:"host"`
}

type caddyForward struct {
	Handler   string              `json:"handler"`
	Upstreams []caddyDial         `json:"upstreams,omitempty"`
	Status    int                 `json:"status_code,omitempty"`
	Headers   map[string][]string `json:"headers,omitempty"`
}

type caddyDial struct {
	Dial string `json:"dial"`
}

func forwarding(identity, upstream string) caddyRoute {
	return caddyRoute{
		Identity: identity,
		Handle: []caddyForward{{
			Handler:   "reverse_proxy",
			Upstreams: []caddyDial{{Dial: upstream}},
		}},
	}
}

func refusing(identity string) caddyRoute {
	return caddyRoute{
		Identity: identity,
		Handle: []caddyForward{{
			Handler: "static_response",
			Status:  http.StatusNotFound,
			Headers: map[string][]string{EdgeHeader: {EdgeName}},
		}},
	}
}

func matching(identity string, hostnames []string, upstream string) caddyRoute {
	route := forwarding(identity, upstream)
	route.Match = []caddyMatch{{Host: append([]string{}, hostnames...)}}
	return route
}

func RenderProxyConfig(state ProxyState) ([]byte, error) {
	seeded, err := seededConfig()
	if err != nil {
		return nil, err
	}
	routes := make([]caddyRoute, 0, len(state.Claims)+len(state.Routes)+1)
	claimed := map[string][]string{}
	for _, claim := range slices.SortedFunc(slices.Values(state.Claims), func(a, b HostClaim) int {
		return strings.Compare(a.Hostname, b.Hostname)
	}) {
		if err := validClaim(claim); err != nil {
			return nil, err
		}
		routes = append(routes, caddyRoute{
			Identity: claimIdentity + claim.Owner + claimSeparator + claim.Hostname,
			Match:    []caddyMatch{{Host: []string{claim.Hostname}}},
			Handle:   []caddyForward{},
		})
		claimed[claim.Owner] = append(claimed[claim.Owner], claim.Hostname)
	}
	answering := map[string]AppRoute{}
	for _, route := range slices.SortedFunc(slices.Values(state.Routes), byKey) {
		if err := validRoute(route); err != nil {
			return nil, err
		}
		hostnames := claimed[route.Owner]
		if len(hostnames) > 0 {
			if held, taken := answering[route.Owner]; taken {
				return nil, providerkit.Refuse(providerkit.CodeInvalid,
					"%s claims %s on this box and would forward it to %s and %s alike: a reverse proxy handler is terminal and these routes are written in name order, so %s answers every hostname this surface claims and %s is configuration nothing reaches. A box points a hostname at one app; give each app a domain of its own, or split them into a project each",
					route.Owner, strings.Join(hostnames, ", "), held.App, route.App, held.App, route.App)
			}
			answering[route.Owner] = route
		}
		routes = append(routes, matching(route.identity(), hostnames, route.Upstream))
	}
	routes = append(routes, refusing(boxIdentity))

	live := seeded.Apps.HTTP.Servers[proxyServer]
	live.Routes = routes
	rendered := caddyConfig{
		Admin:   caddyAdmin{Listen: caddyadmin.Listen(ProxyAdminSocket)},
		Logging: seeded.Logging,
		Apps: caddyApps{HTTP: caddyHTTP{
			GracePeriod: spelled(state.Grace),
			Servers:     map[string]caddyServer{proxyServer: live},
		}},
	}
	if state.Retiring != "" {
		rendered.Apps.HTTP.Servers[proxyDrainServer] = caddyServer{
			Listen: []string{drainListen},
			Routes: []caddyRoute{forwarding(drainIdentity, state.Retiring)},
		}
	}
	return json.Marshal(rendered)
}

func ReadProxyState(document []byte) (ProxyState, error) {
	read, err := parsed(document)
	if err != nil {
		return ProxyState{}, err
	}
	grace, err := time.ParseDuration(read.Apps.HTTP.GracePeriod)
	if err != nil {
		return ProxyState{}, providerkit.Refuse(providerkit.CodeInvalid,
			"%s declares its grace period as %q, and the deploy's drain ceiling and the proxy's are one number",
			ProxyConfig, read.Apps.HTTP.GracePeriod)
	}
	for _, named := range slices.Sorted(maps.Keys(read.Apps.HTTP.Servers)) {
		if named != proxyServer && named != proxyDrainServer {
			return ProxyState{}, unwritten("server", named)
		}
	}
	state := ProxyState{Grace: grace}
	for _, route := range read.Apps.HTTP.Servers[proxyServer].Routes {
		if route.Identity == boxIdentity {
			continue
		}
		if claim, mine := strings.CutPrefix(route.Identity, claimIdentity); mine {
			owner, hostname, split := strings.Cut(claim, claimSeparator)
			if !split || !claimsOnly(route, hostname) {
				return ProxyState{}, unwritten("route", route.Identity)
			}
			state.Claims = append(state.Claims, HostClaim{Hostname: hostname, Owner: owner})
			continue
		}
		named, keyed := strings.CutPrefix(route.Identity, routeIdentity)
		fields := strings.Split(named, claimSeparator)
		if !keyed || len(fields) != 3 || len(route.Handle) == 0 || len(route.Handle[0].Upstreams) == 0 {
			return ProxyState{}, unwritten("route", route.Identity)
		}
		state.Routes = append(state.Routes, AppRoute{
			RouteKey: RouteKey{Owner: fields[0], Pointer: fields[1], App: fields[2]},
			Upstream: route.Handle[0].Upstreams[0].Dial,
		})
	}
	return state, nil
}

func byKey(a, b AppRoute) int {
	return strings.Compare(a.identity(), b.identity())
}

func validRoute(route AppRoute) error {
	for _, field := range []struct{ what, named string }{
		{"surface", route.Owner}, {"pointer", route.Pointer}, {"app", route.App},
	} {
		what, named := field.what, field.named
		if named == "" || strings.Contains(named, claimSeparator) {
			return providerkit.Refuse(providerkit.CodeInvalid,
				"a route on this box names %s %q, and %s tells one surface's route from another's out of the surface, the pointer and the app together",
				what, named, ProxyConfig)
		}
	}
	if route.Upstream == "" {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the route %s names no upstream to forward to", route.identity())
	}
	return nil
}

func unwritten(what, named string) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s carries a %s ocel did not write (%q), and a deploy that rewrites this file whole would take it with it",
		ProxyConfig, what, named)
}

func claimsOnly(route caddyRoute, hostname string) bool {
	return len(route.Handle) == 0 && len(route.Match) == 1 && slices.Equal(route.Match[0].Host, []string{hostname})
}

func validClaim(claim HostClaim) error {
	switch {
	case claim.Hostname == "" || claim.Owner == "":
		return providerkit.Refuse(providerkit.CodeInvalid,
			"a hostname claim on this box names host %q and surface %q, and %s answers which surface claims a host out of both",
			claim.Hostname, claim.Owner, ProxyConfig)
	case strings.Contains(claim.Owner, claimSeparator) || strings.Contains(claim.Hostname, claimSeparator):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the claim of %q by %q carries %q, which is what separates the surface from the host it claims",
			claim.Hostname, claim.Owner, claimSeparator)
	}
	return nil
}

func seededConfig() (caddyConfig, error) {
	read, err := parsed(proxyBaseline)
	if err != nil {
		return caddyConfig{}, err
	}
	if _, seeded := read.Apps.HTTP.Servers[proxyServer]; !seeded {
		return caddyConfig{}, providerkit.Refuse(providerkit.CodeInvalid,
			"the baseline config declares no %s server, and every route a deploy renders hangs off it", proxyServer)
	}
	return read, nil
}

func parsed(document []byte) (caddyConfig, error) {
	var read caddyConfig
	if err := json.Unmarshal(document, &read); err != nil {
		return caddyConfig{}, providerkit.Refuse(providerkit.CodeInvalid,
			"%s is not the json a proxy configuration is read out of: %v", ProxyConfig, err)
	}
	return read, nil
}

func spelled(window time.Duration) string {
	return fmt.Sprintf("%ds", int(window.Round(time.Second).Seconds()))
}
