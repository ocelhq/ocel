package host

import (
	"encoding/json"
	"fmt"
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
)

const (
	DeployWindow = 60 * time.Second
	DrainWindow  = 30 * time.Second
)

type AppRoute struct {
	App      string
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

func Claiming(claims []HostClaim, taken HostClaim) []HostClaim {
	kept := slices.DeleteFunc(slices.Clone(claims), func(claim HostClaim) bool { return claim.Hostname == taken.Hostname })
	return append(kept, taken)
}

func Disclaiming(claims []HostClaim, hostname string) []HostClaim {
	return slices.DeleteFunc(slices.Clone(claims), func(claim HostClaim) bool { return claim.Hostname == hostname })
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
	Host []string `json:"host,omitempty"`
}

type caddyForward struct {
	Handler   string      `json:"handler"`
	Upstreams []caddyDial `json:"upstreams"`
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

func RenderProxyConfig(state ProxyState) ([]byte, error) {
	seeded, err := seededConfig()
	if err != nil {
		return nil, err
	}
	routes := make([]caddyRoute, 0, len(state.Claims)+len(state.Routes))
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
	}
	for _, route := range slices.SortedFunc(slices.Values(state.Routes), func(a, b AppRoute) int {
		return strings.Compare(a.App, b.App)
	}) {
		routes = append(routes, forwarding(routeIdentity+route.App, route.Upstream))
	}

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
	state := ProxyState{Grace: grace}
	for _, route := range read.Apps.HTTP.Servers[proxyServer].Routes {
		if claim, mine := strings.CutPrefix(route.Identity, claimIdentity); mine {
			owner, hostname, split := strings.Cut(claim, claimSeparator)
			if !split || !claimsOnly(route, hostname) {
				return ProxyState{}, unwritten(route.Identity)
			}
			state.Claims = append(state.Claims, HostClaim{Hostname: hostname, Owner: owner})
			continue
		}
		app, named := strings.CutPrefix(route.Identity, routeIdentity)
		if !named || len(route.Handle) == 0 || len(route.Handle[0].Upstreams) == 0 {
			return ProxyState{}, unwritten(route.Identity)
		}
		state.Routes = append(state.Routes, AppRoute{App: app, Upstream: route.Handle[0].Upstreams[0].Dial})
	}
	return state, nil
}

func unwritten(identity string) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s carries a route ocel did not write (%q), and a deploy that rewrites this file whole would take it with it",
		ProxyConfig, identity)
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
	case strings.Contains(claim.Owner, claimSeparator):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the surface %q claiming %s carries %q, which separates the surface from the host it claims",
			claim.Owner, claim.Hostname, claimSeparator)
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
