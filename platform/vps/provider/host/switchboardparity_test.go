package host

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

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
		"one hostname claimed twice in different cases": claimedBy(routed(),
			HostClaim{Hostname: claimed, Owner: surface, Pointer: pointed},
			HostClaim{Hostname: strings.ToUpper(claimed), Owner: surface, Pointer: pointed, App: "web"}),
		"a route naming no upstream": {Grace: DrainWindow, Routes: []AppRoute{{RouteKey: keyed("web")}}},
		"a route to an upstream with no port": {Grace: DrainWindow,
			Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: "web"}}},
		"a route to an upstream over a unix socket": {Grace: DrainWindow,
			Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: "unix//run/web.sock"}}},
		"a preview base":           previewing(),
		"a one-label preview base": {Grace: DrainWindow, PreviewBase: "localhost"},
		"the connector":            connected,
	}
}

func TestTheProviderRefusesExactlyTheRoutingTablesTheSwitchboardRefuses(t *testing.T) {
	t.Parallel()

	for what, table := range parityTables() {
		_, renderErr := RenderProxyConfig(table)
		_, readErr := switchboard.Read(mustWrite(t, table))
		if (renderErr == nil) != (readErr == nil) {
			t.Errorf("%s: the provider says %v and the switchboard says %v, want both to refuse or both to take it: a table the provider writes and the switchboard cannot load strands the box on its old routes", what, renderErr, readErr)
		}
	}
}
