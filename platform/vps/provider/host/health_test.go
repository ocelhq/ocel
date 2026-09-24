package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/caddyadmin"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func activeCheck(t *testing.T, rendered []byte, identity string) (caddyForward, bool) {
	t.Helper()
	var read caddyConfig
	if err := json.Unmarshal(rendered, &read); err != nil {
		t.Fatal(err)
	}
	for _, server := range read.Apps.HTTP.Servers {
		for _, route := range server.Routes {
			if route.Identity != identity {
				continue
			}
			for _, handled := range route.Handle {
				if handled.Handler == caddyadmin.ForwardHandler {
					return handled, true
				}
			}
		}
	}
	return caddyForward{}, false
}

func TestAnAppRouteProbesItsHealthPathActively(t *testing.T) {
	t.Parallel()

	state := releasing()
	state.Routes[0].Health = "/healthz"
	rendered := mustRender(t, state)

	app, found := activeCheck(t, rendered, state.Routes[0].identity())
	if !found || app.HealthChecks == nil {
		t.Fatalf("the app route forwards with no health check, so a container that stops answering stays in rotation until the next deploy:\n%s", rendered)
	}
	active := app.HealthChecks.Active
	if active.URI != "/healthz" || active.ExpectStatus != healthExpects || active.Interval != spelled(healthInterval) || active.Timeout != spelled(healthTimeout) {
		t.Errorf("the app route probes %+v, want the deploy's own health path every %s, timing out at %s and taking any 2xx as up, the way the deploy gate does", active, healthInterval, healthTimeout)
	}
	if strings.Contains(string(rendered), `"path":"/healthz"`) {
		t.Error("the check names its path under the deprecated `path` key rather than `uri`")
	}

	read, err := ReadRoutingTable(mustWrite(t, state))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
	}
	if len(read.Routes) != 1 || read.Routes[0] != state.Routes[0] {
		t.Errorf("the route read back as %+v, want %+v: a deploy that cannot read a neighbour's health path rewrites the box without it", read.Routes, state.Routes)
	}
}

func TestARouteWithNoHealthPathRendersNoCheckAndReadsBackAsNone(t *testing.T) {
	t.Parallel()

	rendered := mustRender(t, releasing())
	app, found := activeCheck(t, rendered, releasing().Routes[0].identity())
	if !found || app.HealthChecks != nil {
		t.Errorf("a route naming no health path renders %+v", app.HealthChecks)
	}
	read, err := ReadRoutingTable(mustWrite(t, releasing()))
	if err != nil || read.Routes[0].Health != "" {
		t.Errorf("ReadRoutingTable() = %+v, %v", read.Routes, err)
	}
}

func TestAHealthPathTheProxyCouldNotProbeIsRefusedRatherThanRendered(t *testing.T) {
	t.Parallel()

	if _, err := RenderProxyConfig(RoutingTable{Grace: DrainWindow, Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: "shop-web-1:8080", Health: "healthz"}}}); err == nil {
		t.Error("a health path with no leading slash rendered, and the proxy would refuse the whole configuration at load")
	}
}

func TestAReleaseWritesTheHealthPathItGatedOnIntoTheRouteItFlipsTo(t *testing.T) {
	t.Parallel()

	stood, err := released(t, aRelease(), session.Result{}, session.Result{}, nil)
	if err != nil {
		t.Fatalf("Release() = %v", err)
	}
	state, err := ReadRoutingTable([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Routes) != 1 || state.Routes[0].Health != aRelease().Apps[0].HealthPath {
		t.Errorf("the release left the route as %+v, want it probing %s: the path the deploy gated on is the one the proxy keeps checking", state.Routes, aRelease().Apps[0].HealthPath)
	}
	if _, err := stood.host().Serving(context.Background(), aRelease().Apps[0].RouteKey); err != nil {
		t.Errorf("Serving() over the released configuration = %v", err)
	}
}
