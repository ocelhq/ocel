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
	drainIdentity    = "ocel-retiring"
)

type AppRoute struct {
	App      string
	Upstream string
}

type ProxyState struct {
	Grace    time.Duration
	Routes   []AppRoute
	Retiring string
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
	Handle   []caddyForward `json:"handle"`
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
	routes := make([]caddyRoute, 0, len(state.Routes))
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
		app, named := strings.CutPrefix(route.Identity, routeIdentity)
		if !named || len(route.Handle) == 0 || len(route.Handle[0].Upstreams) == 0 {
			return ProxyState{}, providerkit.Refuse(providerkit.CodeInvalid,
				"%s carries a route ocel did not write (%q), and a deploy that rewrites this file whole would take it with it",
				ProxyConfig, route.Identity)
		}
		state.Routes = append(state.Routes, AppRoute{App: app, Upstream: route.Handle[0].Upstreams[0].Dial})
	}
	return state, nil
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
