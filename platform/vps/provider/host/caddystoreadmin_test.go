package host

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

type renderedRoute struct {
	Identity string `json:"@id"`
	Match    []struct {
		Host []string `json:"host"`
		Path []string `json:"path"`
	} `json:"match"`
	Handle []struct {
		Handler string `json:"handler"`
		Status  int    `json:"status_code"`
	} `json:"handle"`
}

func renderedRoutes(t *testing.T, rendered []byte) []renderedRoute {
	t.Helper()
	var read struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []renderedRoute `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(rendered, &read); err != nil {
		t.Fatal(err)
	}
	return read.Apps.HTTP.Servers[proxyServer].Routes
}

func TestTheStoresAdminApiIsRefusedAtTheEdgeAheadOfItsForward(t *testing.T) {
	t.Parallel()

	routes := renderedRoutes(t, mustRender(t, storing()))
	forward := slices.IndexFunc(routes, func(r renderedRoute) bool {
		return r.Identity == keyed(live.StoreLabel).identity()
	})
	refusal := slices.IndexFunc(routes, func(r renderedRoute) bool {
		return r.Identity == keyed(live.StoreLabel).identity()+storeAdminSuffix
	})
	if refusal < 0 {
		t.Fatalf("nothing stands between the internet and the store's admin api:\n%+v", routes)
	}
	if refusal > forward {
		t.Fatalf("the store's forward is terminal and comes first, so the refusal at %d is configuration nothing reaches", refusal)
	}
	held := routes[refusal]
	if len(held.Match) != 1 || !slices.Contains(held.Match[0].Host, "storage.shop.example.com") {
		t.Errorf("the refusal answers %+v, want the hostname the store is claimed under", held.Match)
	}
	for _, path := range []string{"/rustfs/*", "/health/*"} {
		if !slices.Contains(held.Match[0].Path, path) {
			t.Errorf("the refusal lets %s through to the store, and that is its admin api and its liveness on the open internet", path)
		}
	}
	if len(held.Handle) != 1 || held.Handle[0].Handler != refuseHandler || held.Handle[0].Status != 404 {
		t.Errorf("the refusal is %+v, want a terminal static response", held.Handle)
	}
}

func TestTheStoresAdminRefusalSurvivesBeingWrittenAndReadBack(t *testing.T) {
	t.Parallel()

	state, err := ReadRoutingTable(mustWrite(t, storing()))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
	}
	again, err := RenderProxyConfig(state)
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}
	if string(again) != string(mustRender(t, storing())) {
		t.Error("a deploy that read this box's own config back and wrote it whole would drop the store's admin refusal")
	}
}
