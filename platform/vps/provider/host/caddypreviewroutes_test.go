package host

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const previewSurface = "ocel--shop--preview"

func previewKey(pointer, app string) RouteKey {
	return RouteKey{Owner: previewSurface, Pointer: pointer, App: app}
}

func previewClaim(pointer, app, hostname string) HostClaim {
	return HostClaim{Hostname: hostname, Owner: previewSurface, Pointer: pointer, App: app}
}

func twoBranchesOfOneApp() ProxyState {
	return ProxyState{
		Grace:       DrainWindow,
		PreviewBase: previewBase,
		Routes: []AppRoute{
			{RouteKey: previewKey("pr-7", "web"), Upstream: "shop-web-7:" + AppPort},
			{RouteKey: previewKey("pr-9", "web"), Upstream: "shop-web-9:" + AppPort},
		},
		Claims: []HostClaim{
			previewClaim("pr-7", "", "shop--pr-7."+previewBase),
			previewClaim("pr-9", "", "shop--pr-9."+previewBase),
		},
	}
}

func TestTwoLivePreviewsOfOneAppAnswerOnTheirOwnHostnameEach(t *testing.T) {
	t.Parallel()

	rendered, err := RenderProxyConfig(twoBranchesOfOneApp())
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v: two branches of one app are the ordinary preview case, and a claim keyed on the surface and the app alone hands both hostnames to both routes", err)
	}
	read, err := ReadProxyState(rendered)
	if err != nil {
		t.Fatalf("ReadProxyState() = %v", err)
	}
	want := slices.SortedFunc(slices.Values(twoBranchesOfOneApp().Claims), func(a, b HostClaim) int {
		return strings.Compare(a.Hostname, b.Hostname)
	})
	if !slices.Equal(read.Claims, want) {
		t.Fatalf("the claims read back as %v, want %v: the pointer is what tells one branch's hostname from another's", read.Claims, want)
	}
	answered := map[string][]string{}
	for _, route := range routesOf(t, rendered) {
		named, keyed := strings.CutPrefix(route.Identity, routeIdentity)
		if !keyed || len(route.Match) != 1 {
			continue
		}
		pointer := strings.Split(named, claimSeparator)[1]
		answered[pointer] = route.Match[0].Host
	}
	for _, claim := range twoBranchesOfOneApp().Claims {
		if !slices.Equal(answered[claim.Pointer], []string{claim.Hostname}) {
			t.Errorf("the route of branch %s answers %v, want exactly [%s]: a reverse proxy handler is terminal, so a route carrying the other branch's hostname takes a live preview off the air, and a route carrying none serves nothing at all",
				claim.Pointer, answered[claim.Pointer], claim.Hostname)
		}
	}
}

func routesOf(t *testing.T, rendered []byte) []caddyRoute {
	t.Helper()

	read, err := parsed(rendered)
	if err != nil {
		t.Fatal(err)
	}
	return read.Apps.HTTP.Servers[proxyServer].Routes
}

func TestAClaimNamingNoPointerIsRefusedRatherThanAnsweredForEveryBranch(t *testing.T) {
	t.Parallel()

	state := twoBranchesOfOneApp()
	state.Claims[0].Pointer = ""
	if _, err := RenderProxyConfig(state); err == nil {
		t.Error("a claim naming no pointer rendered, and a box runs many branches of one app at once: the pointer is half of what says which route answers a hostname")
	}
	if err := validClaim(previewClaim("pr"+claimSeparator+"7", "", "shop--pr-7."+previewBase)); err == nil {
		t.Errorf("a pointer carrying %q is claimable, and it is what separates the fields of the identity this is written under", claimSeparator)
	}
}

func TestAProductionBindClaimsUnderTheDefaultPointerAndKeepsItsRoute(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: edge.DefaultPointer}}
	read, err := ReadProxyState(mustRender(t, state))
	if err != nil {
		t.Fatalf("ReadProxyState() = %v", err)
	}
	if !slices.Equal(read.Claims, state.Claims) {
		t.Fatalf("the claims read back as %v, want %v", read.Claims, state.Claims)
	}
	at := slices.IndexFunc(routesOf(t, mustRender(t, state)), func(route caddyRoute) bool {
		return strings.HasPrefix(route.Identity, routeIdentity)
	})
	if at < 0 {
		t.Fatal("the box renders no forwarding route for a claimed hostname")
	}
}

func twoAppsOfOneBranch(web, api string) ProxyState {
	return ProxyState{
		Grace:       DrainWindow,
		PreviewBase: previewBase,
		Routes: []AppRoute{
			{RouteKey: previewKey("pr-7", "web"), Upstream: web},
			{RouteKey: previewKey("pr-7", "api"), Upstream: api},
		},
		Claims: []HostClaim{
			previewClaim("pr-7", "web", "shop--pr-7--web."+previewBase),
			previewClaim("pr-7", "api", "shop--pr-7--api."+previewBase),
		},
	}
}

func TestARealProxyServesOnePreviewHostPerAppAndOnePerBranch(t *testing.T) {
	network := standingNetwork(t)
	web := standingAppOn(t, network, "web", "the web preview answered")
	api := standingAppOn(t, network, "api", "the api preview answered")

	t.Run("one hostname per branch", func(t *testing.T) {
		state := twoBranchesOfOneApp()
		for at := range state.Routes {
			state.Routes[at].Upstream = web
		}
		ask := probingConfig(t, issuedByNobody(t, mustRender(t, state)), network)

		for _, claim := range state.Claims {
			if said := ask(claim.Hostname); said.body != "the web preview answered" {
				t.Errorf("%s was answered %d %q, want the app the branch that claimed it runs", claim.Hostname, said.status, said.body)
			}
		}
		if said := ask("shop--pr-4." + previewBase); said.status != http.StatusNotFound || said.body != "" {
			t.Errorf("a branch nothing on this box runs was answered %d %q, want the catch-all's bare 404", said.status, said.body)
		}
	})

	t.Run("one hostname per app", func(t *testing.T) {
		ask := probingConfig(t, issuedByNobody(t, mustRender(t, twoAppsOfOneBranch(web, api))), network)

		for hostname, want := range map[string]string{
			"shop--pr-7--web." + previewBase: "the web preview answered",
			"shop--pr-7--api." + previewBase: "the api preview answered",
		} {
			if said := ask(hostname); said.body != want {
				t.Errorf("%s was answered %d %q, want %q: a reverse proxy handler is terminal, so a preview of a multi-app project whose hostnames land on one route serves whichever app sorted first under both of its names",
					hostname, said.status, said.body, want)
			}
		}
		if said := ask("shop--pr-7." + previewBase); said.status != http.StatusNotFound || said.body != "" {
			t.Errorf("the app-less hostname of a two-app preview was answered %d %q, want the catch-all's bare 404: a project of two apps claims one hostname per app and no name without one", said.status, said.body)
		}
	})
}
