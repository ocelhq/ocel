package host

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func everything() RoutingTable {
	table := storing()
	table.Routes = []AppRoute{
		{RouteKey: keyed(switchboard.StoreLabel), Upstream: "shop-prod-store-s3:9000"},
		{RouteKey: keyed("web"), Upstream: "shop-web-1:" + providerkit.InjectedPortText},
	}
	table.Grace = 12 * time.Second
	table.Pins = []Pin{{Hostname: "shop.example.com", Path: caddy.PinsDir + "/shop"}}
	table.PreviewBase = previewBase
	table.Connector = "box.example.com"
	return table
}

func mustWrite(t *testing.T, table RoutingTable) []byte {
	t.Helper()
	written, err := WriteRoutingTable(table)
	if err != nil {
		t.Fatalf("WriteRoutingTable() = %v", err)
	}
	return written
}

func TestTheRoutingTableReadsBackAsTheTableThatWroteIt(t *testing.T) {
	t.Parallel()

	for what, table := range map[string]RoutingTable{
		"a box serving nothing":            {Grace: DrainWindow},
		"a box carrying every kind of row": everything(),
	} {
		read, err := ReadRoutingTable(mustWrite(t, table))
		if err != nil {
			t.Fatalf("ReadRoutingTable() over %s = %v", what, err)
		}
		if !reflect.DeepEqual(read, table) {
			t.Errorf("%s reads back as\n%+v\nwant\n%+v: a deploy composes the next table off what it reads, and a row lost here is a row the next write drops", what, read, table)
		}
		if !bytes.Equal(mustRender(t, read), mustRender(t, table)) {
			t.Errorf("%s renders differently once read back, and the proxy config is only ever rendered off the table", what)
		}
	}
}

func TestTheRoutingTableSpeaksNoProxysVocabulary(t *testing.T) {
	t.Parallel()

	written := string(mustWrite(t, everything()))
	for _, caddy := range []string{"@id", "apps", "servers", "handle", "reverse_proxy", "dial", "load_files", "grace_period", "ocel-app-", "ocel-host-"} {
		if strings.Contains(written, `"`+caddy) {
			t.Errorf("the routing table carries %q, a word of the proxy it is rendered into:\n%s", caddy, written)
		}
	}
	if written != strings.ToLower(written) {
		t.Errorf("the routing table spells a field in capitals:\n%s", written)
	}
}

func TestATableWrittenFromRowsInAnyOrderIsTheSameBytes(t *testing.T) {
	t.Parallel()

	table := everything()
	table.Pins = append(table.Pins, Pin{Hostname: "blog.example.com", Path: caddy.PinsDir + "/blog"})
	scrambled := table
	scrambled.Claims = []HostClaim{table.Claims[1], table.Claims[0]}
	scrambled.Routes = []AppRoute{table.Routes[1], table.Routes[0]}
	scrambled.Pins = []Pin{table.Pins[1], table.Pins[0]}
	if !bytes.Equal(mustWrite(t, scrambled), mustWrite(t, table)) {
		t.Error("the same rows handed over in another order write another table, and a deploy that changes nothing then rewrites the box and reloads its proxy")
	}
}

func TestATableOcelCouldNotRenderIsRefusedWhenItIsRead(t *testing.T) {
	t.Parallel()

	for what, written := range map[string]string{
		"a table that is not json":        `{"grace":`,
		"a field ocel never writes":       `{"grace":"30s","upstream":"shop-web-1:8080"}`,
		"an unreadable grace":             `{"grace":"soon"}`,
		"a claim of a wildcard":           `{"grace":"30s","claims":[{"hostname":"*.example.com","owner":"ocel--shop--production","pointer":"@production"}]}`,
		"a route naming nothing to dial":  `{"grace":"30s","routes":[{"owner":"ocel--shop--production","pointer":"@production","app":"web"}]}`,
		"a pin the proxy could not open":  `{"grace":"30s","pins":[{"hostname":"shop.example.com","path":"/srv/certs/shop"}]}`,
		"a preview base of one dns label": `{"grace":"30s","preview":"internal"}`,
	} {
		var refusal refusal.Refusal
		if _, err := ReadRoutingTable([]byte(written)); !errors.As(err, &refusal) || !strings.Contains(err.Error(), live.RoutingTable) {
			t.Errorf("ReadRoutingTable() over %s = %v, want a refusal naming %s: a row ocel cannot render takes every later reshape on this box with it",
				what, err, live.RoutingTable)
		}
	}
}

func TestWhatTheBoxsAgentReadsItsStoreBaseFromIsTheTableADeployWrote(t *testing.T) {
	t.Parallel()

	claims, err := live.ClaimedIn(mustWrite(t, storing()))
	if err != nil {
		t.Fatalf("ClaimedIn() = %v", err)
	}
	if base := live.StoreBase(claims, surface, pointed); base != "https://storage.shop.example.com" {
		t.Errorf("the agent reads a store base of %q off the table a deploy wrote, want https://storage.shop.example.com", base)
	}
	if len(claims) != len(storing().Claims) {
		t.Errorf("the agent reads %d claims off a table holding %d", len(claims), len(storing().Claims))
	}
}
