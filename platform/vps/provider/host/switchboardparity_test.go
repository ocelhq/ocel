package host

import (
	"encoding/json"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func renderedAnswer(routes []caddyRoute, host, path string) (switchboard.Forward, bool) {
	for _, route := range routes {
		if !caddyMatches(route.Match, host, path) || len(route.Handle) == 0 {
			continue
		}
		strip := ""
		for _, handler := range route.Handle {
			switch handler.Handler {
			case refuseHandler:
				return switchboard.Forward{}, false
			case rewriteHandler:
				strip = handler.StripPathPrefix
			case "reverse_proxy":
				return switchboard.Forward{Upstream: handler.Upstreams[0].Dial, Strip: strip}, true
			}
		}
	}
	return switchboard.Forward{}, false
}

func caddyMatches(sets []caddyMatch, host, path string) bool {
	if len(sets) == 0 {
		return true
	}
	for _, set := range sets {
		hosted := set.Host == nil
		if set.Host != nil {
			for _, pattern := range *set.Host {
				hosted = hosted || caddyHost(pattern, host)
			}
		}
		pathed := len(set.Path) == 0
		for _, pattern := range set.Path {
			if prefix, wild := strings.CutSuffix(pattern, "*"); wild {
				pathed = pathed || strings.HasPrefix(strings.ToLower(path), strings.ToLower(prefix))
				continue
			}
			pathed = pathed || strings.EqualFold(pattern, path)
		}
		if hosted && pathed {
			return true
		}
	}
	return false
}

func caddyHost(pattern, host string) bool {
	if base, wild := strings.CutPrefix(pattern, "*."); wild {
		label, under := strings.CutSuffix(strings.ToLower(host), "."+strings.ToLower(base))
		return under && label != "" && !strings.Contains(label, ".")
	}
	return strings.EqualFold(pattern, host)
}

func parityTables() map[string]RoutingTable {
	claimedBy := func(state RoutingTable, claims ...HostClaim) RoutingTable {
		state.Claims = append(state.Claims, claims...)
		return state
	}
	twoApps := func() RoutingTable {
		state := routed()
		state.Routes = append(state.Routes, AppRoute{RouteKey: keyed("api"), Upstream: "shop-api-1:3000"})
		return state
	}
	connected := RoutingTable{
		Grace:     DeployWindow,
		Connector: "box.example.com",
		Claims:    []HostClaim{{Hostname: "box.example.com", Owner: surface, Pointer: pointed, App: "web"}},
		Routes:    []AppRoute{{RouteKey: keyed("web"), Upstream: "web:3000"}},
	}
	return map[string]RoutingTable{
		"a box serving nothing":                   {Grace: DrainWindow},
		"a box carrying every kind of row":        everything(),
		"a store beside an app":                   storing(),
		"a project-wide claim on one app":         claimedBy(routed(), HostClaim{Hostname: claimed, Owner: surface, Pointer: pointed}),
		"an app's own claim on a two-app surface": claimedBy(twoApps(), HostClaim{Hostname: claimed, Owner: surface, Pointer: pointed, App: "api"}),
		"a project-wide claim on two apps":        claimedBy(twoApps(), HostClaim{Hostname: claimed, Owner: surface, Pointer: pointed}),
		"one hostname claimed twice": claimedBy(routed(),
			HostClaim{Hostname: claimed, Owner: surface, Pointer: pointed},
			HostClaim{Hostname: claimed, Owner: surface, Pointer: pointed, App: "web"}),
		"a claim no route answers":   claimedBy(routed(), HostClaim{Hostname: claimed, Owner: surface, Pointer: "@pr-1"}),
		"a wildcard claim":           claimedBy(routed(), HostClaim{Hostname: "*.example.com", Owner: surface, Pointer: pointed}),
		"a claim with the separator": claimedBy(routed(), HostClaim{Hostname: claimed, Owner: surface + "/x", Pointer: pointed}),
		"a claim naming no pointer":  claimedBy(routed(), HostClaim{Hostname: claimed, Owner: surface}),
		"a route naming no upstream": {Grace: DrainWindow, Routes: []AppRoute{{RouteKey: keyed("web")}}},
		"a route with a relative health path": {Grace: DrainWindow,
			Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: "web:3000", Health: "up"}}},
		"a preview base":           previewing(),
		"a one-label preview base": {Grace: DrainWindow, PreviewBase: "localhost"},
		"the connector":            connected,
	}
}

func TestTheSwitchboardAnswersEveryHostnameAndPathTheWayTheRenderedConfigDoes(t *testing.T) {
	t.Parallel()

	for what, table := range parityTables() {
		rendered, renderErr := RenderProxyConfig(table)
		read, readErr := switchboard.Read(mustWrite(t, table))
		if (renderErr == nil) != (readErr == nil) {
			t.Errorf("%s: the renderer says %v and the switchboard says %v, want both to refuse or both to take it", what, renderErr, readErr)
			continue
		}
		if renderErr != nil {
			continue
		}
		var config caddyConfig
		if err := json.Unmarshal(rendered, &config); err != nil {
			t.Fatal(err)
		}
		routes := config.Apps.HTTP.Servers[proxyServer].Routes
		hosts := []string{"unclaimed.example.com", table.Connector}
		for _, held := range table.Claims {
			hosts = append(hosts, held.Hostname, strings.ToUpper(held.Hostname))
		}
		if table.PreviewBase != "" {
			wildcard := edge.PreviewWildcard(table.PreviewBase)
			hosts = append(hosts, "web--pr-9."+table.PreviewBase, "a.b."+table.PreviewBase, edge.ProbeHostname(wildcard))
		}
		for _, host := range hosts {
			for _, path := range []string{"/", "/bucket/key", "/rustfs", "/rustfs/admin", "/HEALTH/ready", "/health",
				ConnectorPath, ConnectorPath + "/connector.v1.Box/Describe", ConnectorPath + "x"} {
				want, wanted := renderedAnswer(routes, host, path)
				got, forwarded := read.Forward(host, path)
				if wanted != forwarded || want != got {
					t.Errorf("%s: %s%s is answered %+v (forwarded %t) by the switchboard, want %+v (forwarded %t) as the rendered config answers it",
						what, host, path, got, forwarded, want, wanted)
				}
			}
		}
	}
}
