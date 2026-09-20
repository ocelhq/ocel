package host

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func storing() ProxyState {
	return ProxyState{
		Grace: DrainWindow,
		Routes: []AppRoute{
			{RouteKey: keyed("web"), Upstream: "shop-web-1:" + providerkit.InjectedPortText},
			{RouteKey: keyed(live.StoreLabel), Upstream: "shop-prod-store-s3:9000"},
		},
		Claims: []HostClaim{
			{Hostname: "shop.example.com", Owner: surface, Pointer: pointed},
			{Hostname: "storage.shop.example.com", Owner: surface, Pointer: pointed, App: live.StoreLabel},
		},
	}
}

func matchedHosts(t *testing.T, rendered []byte, identity string) []string {
	t.Helper()
	var read struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Identity string `json:"@id"`
						Match    []struct {
							Host []string `json:"host"`
						} `json:"match"`
						Handle []struct {
							Upstreams []struct {
								Dial string `json:"dial"`
							} `json:"upstreams"`
						} `json:"handle"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(rendered, &read); err != nil {
		t.Fatal(err)
	}
	for _, route := range read.Apps.HTTP.Servers[proxyServer].Routes {
		if route.Identity != identity {
			continue
		}
		if len(route.Match) != 1 {
			t.Fatalf("%s matches %d host sets", identity, len(route.Match))
		}
		return route.Match[0].Host
	}
	t.Fatalf("%s is not among the routes the box renders", identity)
	return nil
}

func TestAStoreRouteAnswersItsOwnHostnameBesideAnAppClaimingTheProjectsOwn(t *testing.T) {
	t.Parallel()

	rendered := mustRender(t, storing())
	if held := matchedHosts(t, rendered, keyed("web").identity()); len(held) != 1 || held[0] != "shop.example.com" {
		t.Errorf("the app answers %v, want the hostname the project claims with no app of its own", held)
	}
	held := matchedHosts(t, rendered, keyed(live.StoreLabel).identity())
	if len(held) != 1 || held[0] != "storage.shop.example.com" {
		t.Errorf("the store answers %v, want the hostname claimed for it alone: the store is not an app and never answers the project's own name", held)
	}
}

func TestAStoreRouteIsNotAnAppTheProjectsOwnHostnameCouldBeAmbiguousBetween(t *testing.T) {
	t.Parallel()

	state := storing()
	state.Routes = append(state.Routes, AppRoute{RouteKey: keyed("api"), Upstream: "shop-api-1:" + providerkit.InjectedPortText})
	if _, err := RenderProxyConfig(state); err == nil {
		t.Fatal("two apps under one wide claim rendered, and whichever sorted first would answer for both")
	}

	state.Routes = state.Routes[:len(state.Routes)-1]
	if _, err := RenderProxyConfig(state); err != nil {
		t.Fatalf("one app and a store under a wide claim = %v, want the app to answer it", err)
	}
}

func TestTheStoresRouteSurvivesBeingWrittenAndReadBack(t *testing.T) {
	t.Parallel()

	state, err := ReadProxyState(mustRender(t, storing()))
	if err != nil {
		t.Fatalf("ReadProxyState() = %v", err)
	}
	if len(state.Routes) != 2 || len(state.Claims) != 2 {
		t.Fatalf("a box serving an app and a store reads back as %d routes and %d claims", len(state.Routes), len(state.Claims))
	}
	claims, err := live.ClaimedIn(mustRender(t, storing()))
	if err != nil {
		t.Fatalf("ClaimedIn() = %v", err)
	}
	if base := live.StoreBase(claims, surface, pointed); base != "https://storage.shop.example.com" {
		t.Errorf("the box's own config reads back a store base of %q", base)
	}
	_ = time.Second
}
