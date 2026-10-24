//go:build integration

package host

import (
	"net/http"
	"testing"

	"github.com/ocelhq/ocel/pkg/enginetest"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
)

func twoAppsOfOneBranch(web, api string) boxapi.RoutingTable {
	return boxapi.RoutingTable{
		Grace:       routing.DrainWindow,
		PreviewBase: previewBase,
		Routes: []boxapi.Route{
			{RouteKey: previewKey("pr-7", "web"), Upstream: web},
			{RouteKey: previewKey("pr-7", "api"), Upstream: api},
		},
		Claims: []boxapi.Claim{
			previewClaim("pr-7", "web", "shop--pr-7--web."+previewBase),
			previewClaim("pr-7", "api", "shop--pr-7--api."+previewBase),
		},
	}
}

func TestARealProxyServesOnePreviewHostPerAppAndOnePerBranch(t *testing.T) {
	network := enginetest.Network(t, "box")
	web := appOn(t, network, "web", "the web preview answered")
	api := appOn(t, network, "api", "the api preview answered")

	t.Run("one hostname per branch", func(t *testing.T) {
		state := twoBranchesOfOneApp()
		for at := range state.Routes {
			state.Routes[at].Upstream = web
		}
		ask := probingConfig(t, state, issuedByNobody(t, mustRender(t, state)), network)

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
		ask := probingConfig(t, twoAppsOfOneBranch(web, api), issuedByNobody(t, mustRender(t, twoAppsOfOneBranch(web, api))), network)

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
