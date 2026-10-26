package gcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

type forwardedStores struct {
	stores    *googletest.Memorystore
	databases *googletest.CloudSQL
	elsewhere []string
}

func servingStoresToForward(t *testing.T) (*Provider, *forwardedStores) {
	t.Helper()
	stores, storesURL := googletest.StartMemorystore(t)
	target, err := url.Parse(storesURL)
	if err != nil {
		t.Fatal(err)
	}
	toStores := httputil.NewSingleHostReverseProxy(target)
	served := &forwardedStores{stores: stores, databases: googletest.NewCloudSQL()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/locations/"):
			toStores.ServeHTTP(w, r)
		case strings.Contains(r.URL.Path, "/instances"):
			served.databases.Serve(w, r)
		default:
			served.elsewhere = append(served.elsewhere, r.Method+" "+r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return pushing(t, server.URL), served
}

func TestAForwardToAStoreOnAnotherNetworkIsRefusedNamingTheNetwork(t *testing.T) {
	p, served := servingStoresToForward(t)
	labels := storeLabels("production")
	served.stores.HoldingOn(connectedStore, labels, "projects/acme-prod/global/networks/shared")

	_, err := p.ForwardPorts(context.Background(), provider.PortForwardRequest{
		Tier:     "production",
		Bindings: []provider.Binding{{Type: provider.BindingKV, Name: "cache", Properties: map[string]string{provider.PropertyHost: googletest.StoreAddress, provider.PropertyPort: "6379"}}},
	}, nil)

	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid || !strings.Contains(err.Error(), "kv cache") ||
		!strings.Contains(err.Error(), "shared") || !strings.Contains(err.Error(), "a machine on shared") {
		t.Errorf("ForwardPorts() to a store on the network shared = %v, want a refusal naming the store and its network", err)
	}
	if len(served.elsewhere) != 0 {
		t.Errorf("a refused forward reached %v, want no bastion made", served.elsewhere)
	}
}

func TestAForwardToADatabaseOnAnotherNetworkIsRefusedNamingTheNetwork(t *testing.T) {
	p, served := servingStoresToForward(t)
	served.databases.HoldingOn(connectedDatabase, map[string]string{
		"ocel-namespace": "ocel", "ocel-tier": "production", "ocel-project": "shop", "ocel-environment": "prod", "ocel-postgres": "orders",
	}, "projects/acme-prod/global/networks/shared")

	_, err := p.ForwardPorts(context.Background(), provider.PortForwardRequest{
		Tier:     "production",
		Bindings: []provider.Binding{{Type: provider.BindingPostgres, Name: "orders", Properties: map[string]string{provider.PropertyHost: googletest.DatabaseAddress, provider.PropertyPort: "5432"}}},
	}, nil)

	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid || !strings.Contains(err.Error(), "postgres orders") || !strings.Contains(err.Error(), "a machine on shared") {
		t.Errorf("ForwardPorts() to a database on the network shared = %v, want a refusal naming the database and its network", err)
	}
	if len(served.elsewhere) != 0 {
		t.Errorf("a refused forward reached %v, want no bastion made", served.elsewhere)
	}
}

func TestAForwardToAStoreOnTheTiersOwnNetworkGoesOnToTheBastion(t *testing.T) {
	p, served := servingStoresToForward(t)
	served.stores.HoldingOn(connectedStore, storeLabels("production"), "projects/acme-prod/global/networks/ocel-prod-network")

	_, err := p.ForwardPorts(context.Background(), provider.PortForwardRequest{
		Tier:     "production",
		Bindings: []provider.Binding{{Type: provider.BindingKV, Name: "cache", Properties: map[string]string{provider.PropertyHost: googletest.StoreAddress, provider.PropertyPort: "6379"}}},
	}, nil)

	if err == nil || strings.Contains(err.Error(), "a machine on") {
		t.Errorf("ForwardPorts() to a store on the tier's own network = %v, want it taken on to the bastion", err)
	}
	if len(served.elsewhere) == 0 || !strings.Contains(served.elsewhere[0], "/subnetworks/ocel-prod-network") {
		t.Errorf("the forward reached %v, want it to read the tier's subnetwork the bastion forwards into", served.elsewhere)
	}
}
