package boxapi_test

import (
	"slices"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

func TestAHostnameIsTunneledOnlyWhileItsClaimIsSoDroppingTheClaimDropsTheTunnel(t *testing.T) {
	t.Parallel()

	table := boxapi.RoutingTable{Grace: 30 * time.Second, Claims: []boxapi.Claim{
		{Owner: "ocel--shop--production", Hostname: "shop.example.com", Pointer: "@production", App: "web", Tunneled: true},
		{Owner: "ocel--shop--production", Hostname: "storage.shop.example.com", Pointer: "@production", App: boxapi.StoreLabel, Tunneled: true},
	}}
	if !table.IsTunneled("storage.shop.example.com") || slices.Contains(table.Hostnames(), "storage.shop.example.com") {
		t.Fatalf("a store claimed through the tunnel reads tunneled %v and is rendered for the proxy: %v", table.IsTunneled("storage.shop.example.com"), table.Hostnames())
	}
	table.Claims = table.Claims[:1]
	if table.IsTunneled("storage.shop.example.com") {
		t.Error("the store's hostname is still tunneled once nothing claims it, and a tunnel row that outlives its claim strands the hostname")
	}
	table.Claims = nil
	if table.HasTunneled() {
		t.Error("a table that claims nothing still holds the tunnel open")
	}
}

func TestBothSidesRefuseATableWithARowTheSchemaHasNot(t *testing.T) {
	t.Parallel()

	for what, document := range map[string]string{
		"a tunneled list beside the claims": `{"grace":"30s","tunneled":[{"hostname":"shop.example.com","owner":"ocel--shop--production"}]}`,
		"two tables":                        `{"grace":"30s"}{"grace":"30s"}`,
	} {
		if _, err := boxapi.ReadRoutingTable([]byte(document)); err == nil {
			t.Errorf("ReadRoutingTable() over %s = nil, want it refused: the provider and the switchboard read the same rows or none", what)
		}
		if _, err := boxapi.ReadTable([]byte(document)); err == nil {
			t.Errorf("ReadTable() over %s = nil, want it refused", what)
		}
	}
}

func TestAPreviewClaimUnderATunneledPreviewBaseIsNotRenderedForTheProxy(t *testing.T) {
	t.Parallel()

	table := boxapi.RoutingTable{
		Grace:           30 * time.Second,
		PreviewBase:     "preview.example.com",
		PreviewTunneled: true,
		Claims: []boxapi.Claim{
			{Owner: "ocel--shop--pr-1", Hostname: "pr-1.preview.example.com", Pointer: "@pr-1", App: "web"},
		},
	}
	if named := table.Hostnames(); slices.Contains(named, "pr-1.preview.example.com") {
		t.Errorf("Hostnames() = %v, want the preview claim behind the tunneled base left out: the proxy would serve and certify a name whose DNS points at the tunnel", named)
	}
}
