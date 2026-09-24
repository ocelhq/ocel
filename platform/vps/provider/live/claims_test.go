package live_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const claimedTable = `{"grace":"30s","claims":[
	{"owner":"ocel.shop.production","hostname":"shop.example.com","pointer":"@production","app":"web"},
	{"owner":"ocel.shop.production","hostname":"storage.shop.example.com","pointer":"@production","app":"storage"},
	{"owner":"ocel.other.production","hostname":"storage.other.example.com","pointer":"@production","app":"storage"}]}`

func TestTheStoresBaseIsTheHostTheSurfaceClaimsForIt(t *testing.T) {
	t.Parallel()

	claims, err := live.ClaimedIn([]byte(claimedTable))
	if err != nil {
		t.Fatalf("ClaimedIn() = %v", err)
	}
	if base := live.StoreBase(claims, "ocel.shop.production", "@production"); base != "https://storage.shop.example.com" {
		t.Errorf("StoreBase() = %q, want the hostname this surface claims for its store", base)
	}
	if base := live.StoreBase(claims, "ocel.nothing.production", "@production"); base != "" {
		t.Errorf("StoreBase() = %q for a surface that claims nothing, want nothing", base)
	}
}

func TestTheRoutingTableIsNeverWhereTheProxyCanReadIt(t *testing.T) {
	t.Parallel()

	if strings.HasPrefix(live.RoutingTable, live.ProxyDir+"/") {
		t.Errorf("the routing table lives at %s, inside %s, which the proxy container mounts", live.RoutingTable, live.ProxyDir)
	}
}
