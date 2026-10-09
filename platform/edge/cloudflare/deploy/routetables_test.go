package cloudflare

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/router"
)

type routeTableRequest struct {
	method, path, key, authorization string
	body                             []byte
}

func routeTableServer(t *testing.T, status int) (*httptest.Server, func() []routeTableRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []routeTableRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, routeTableRequest{method: r.Method, path: r.URL.Path, key: r.URL.Query().Get("key"), authorization: r.Header.Get("Authorization"), body: body})
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []routeTableRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]routeTableRequest(nil), seen...)
	}
}

func routeTableState(endpoint string) router.StackState {
	return router.NewStackState(edge.StackState{Slug: "shop", Endpoint: endpoint, Secret: "project-secret"})
}

const routeTableKey = "production/shop/web/r1a2b3c4d/route-table/ab.json"

func TestTheRouterStoresARouteTableByPuttingItsExactBytesToTheReleasesStore(t *testing.T) {
	srv, seen := routeTableServer(t, http.StatusNoContent)
	table := []byte(`{"routes":[{"source":"^/(?<slug>[^/]+)$","has":"a&b"}]}`)

	err := NewRouter("ns").Hooks().RouteTables.Store(context.Background(), routeTableState(srv.URL), routeTableKey, table)
	if err != nil {
		t.Fatalf("Store() = %v", err)
	}

	requests := seen()
	if len(requests) != 1 {
		t.Fatalf("the store saw %d requests, want one", len(requests))
	}
	got := requests[0]
	if got.method != http.MethodPut || got.path != "/shop/route-table" || got.key != routeTableKey {
		t.Errorf("request = %s %s?key=%s, want PUT /shop/route-table?key=%s", got.method, got.path, got.key, routeTableKey)
	}
	if got.authorization != "Bearer project-secret" {
		t.Errorf("authorization = %q, want the project secret", got.authorization)
	}
	if !bytes.Equal(got.body, table) {
		t.Errorf("body = %s, want the exact bytes %s: the store checks them against the digest the key names", got.body, table)
	}
}

func TestTheRouterForgetsARouteTableByDeletingItFromTheReleasesStore(t *testing.T) {
	srv, seen := routeTableServer(t, http.StatusNoContent)

	if err := NewRouter("ns").Hooks().RouteTables.Forget(context.Background(), routeTableState(srv.URL), routeTableKey); err != nil {
		t.Fatalf("Forget() = %v", err)
	}

	requests := seen()
	if len(requests) != 1 || requests[0].method != http.MethodDelete || requests[0].path != "/shop/route-table" || requests[0].key != routeTableKey {
		t.Errorf("requests = %+v, want one DELETE /shop/route-table?key=%s", requests, routeTableKey)
	}
}

func TestARouteTableTheReleasesStoreRefusesFailsTheStore(t *testing.T) {
	srv, _ := routeTableServer(t, http.StatusBadRequest)

	err := NewRouter("ns").Hooks().RouteTables.Store(context.Background(), routeTableState(srv.URL), routeTableKey, []byte(`{}`))
	if err == nil {
		t.Fatal("Store() = nil, want the refusal the releases store answered")
	}
}
