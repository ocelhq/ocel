package switchboard_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func mustRead(t *testing.T, document string) *switchboard.Table {
	t.Helper()
	table, err := switchboard.Read([]byte(document))
	if err != nil {
		t.Fatalf("Read(%s) = %v", document, err)
	}
	return table
}

func forwarded(t *testing.T, table *switchboard.Table, host, path string) switchboard.Forward {
	t.Helper()
	forward, ok := table.Forward(host, path)
	if !ok {
		t.Fatalf("%s%s is refused, want it forwarded", host, path)
	}
	return forward
}

func TestAClaimedHostnameForwardsToTheRouteOfTheAppThatClaimedIt(t *testing.T) {
	t.Parallel()

	table := mustRead(t, `{"grace":"30s",
		"claims":[{"owner":"ocel--shop--production","hostname":"shop.example.com","pointer":"@production","app":"web"}],
		"routes":[
			{"owner":"ocel--shop--production","pointer":"@production","app":"api","upstream":"shop-api-1:3000"},
			{"owner":"ocel--shop--production","pointer":"@production","app":"web","upstream":"shop-web-1:3000"}]}`)

	for _, host := range []string{"shop.example.com", "SHOP.Example.com", "shop.example.com:80"} {
		if got := forwarded(t, table, host, "/"); got.Upstream != "shop-web-1:3000" || got.Strip != "" {
			t.Errorf("%s forwards to %+v, want shop-web-1:3000 unstripped", host, got)
		}
	}
}

func TestAProjectWideClaimReachesTheOneAppItsSurfaceRunsAndNeverTheStoreBesideIt(t *testing.T) {
	t.Parallel()

	table := mustRead(t, `{"grace":"30s",
		"claims":[
			{"owner":"ocel--shop--production","hostname":"shop.example.com","pointer":"@production"},
			{"owner":"ocel--shop--production","hostname":"storage.shop.example.com","pointer":"@production","app":"storage"}],
		"routes":[
			{"owner":"ocel--shop--production","pointer":"@production","app":"storage","upstream":"shop-store:9000"},
			{"owner":"ocel--shop--production","pointer":"@production","app":"web","upstream":"shop-web-1:3000"}]}`)

	if got := forwarded(t, table, "shop.example.com", "/"); got.Upstream != "shop-web-1:3000" {
		t.Errorf("the project-wide claim forwards to %s, want the surface's one app", got.Upstream)
	}
	if got := forwarded(t, table, "storage.shop.example.com", "/bucket/key"); got.Upstream != "shop-store:9000" {
		t.Errorf("the store's claim forwards to %s, want the store", got.Upstream)
	}
}

func TestAProjectWideClaimOnASurfaceRunningTwoAppsIsRefusedRatherThanGuessed(t *testing.T) {
	t.Parallel()

	_, err := switchboard.Read([]byte(`{"grace":"30s",
		"claims":[{"owner":"ocel--shop--production","hostname":"shop.example.com","pointer":"@production"}],
		"routes":[
			{"owner":"ocel--shop--production","pointer":"@production","app":"api","upstream":"shop-api-1:3000"},
			{"owner":"ocel--shop--production","pointer":"@production","app":"web","upstream":"shop-web-1:3000"}]}`))
	if err == nil {
		t.Fatal("a hostname claimed by a surface running api and web was read, want it refused: neither app is the one it names")
	}
	for _, named := range []string{"shop.example.com", "api", "web"} {
		if !strings.Contains(err.Error(), named) {
			t.Errorf("the refusal %q does not name %s", err, named)
		}
	}
}

func TestTheStoresAdminApiIsRefusedUnderEverySpellingAndTheBucketsBesideItAreNot(t *testing.T) {
	t.Parallel()

	table := mustRead(t, `{"grace":"30s",
		"claims":[
			{"owner":"ocel--shop--production","hostname":"shop.example.com","pointer":"@production"},
			{"owner":"ocel--shop--production","hostname":"storage.shop.example.com","pointer":"@production","app":"storage"}],
		"routes":[
			{"owner":"ocel--shop--production","pointer":"@production","app":"storage","upstream":"shop-store:9000"},
			{"owner":"ocel--shop--production","pointer":"@production","app":"web","upstream":"shop-web-1:3000"}]}`)

	for _, path := range []string{
		"/rustfs", "/rustfs/", "/rustfs/admin/v3/info", "/health", "/health/ready",
		"/RustFS/admin", "//rustfs/admin", "/bucket/../rustfs/admin", "/rustfs/../bucket",
	} {
		if forward, ok := table.Forward("storage.shop.example.com", path); ok {
			t.Errorf("the store forwards %s to %+v, want it refused: the admin api is never the internet's to reach", path, forward)
		}
	}
	for _, path := range []string{"/bucket/rustfs", "/rustfsx", "/healthy", "/bucket/health"} {
		forwarded(t, table, "storage.shop.example.com", path)
	}
	for _, path := range []string{"/health", "/rustfs/x"} {
		if got := forwarded(t, table, "shop.example.com", path); got.Upstream != "shop-web-1:3000" {
			t.Errorf("the app forwards %s to %s, want the app: only the store's hostnames refuse its admin paths", path, got.Upstream)
		}
	}
}

func TestTheConnectorPathOnTheConnectorsHostnameIsForwardedStrippedToItsSocket(t *testing.T) {
	t.Parallel()

	table := mustRead(t, `{"grace":"30s",
		"claims":[{"owner":"ocel--shop--production","hostname":"box.example.com","pointer":"@production"}],
		"routes":[{"owner":"ocel--shop--production","pointer":"@production","app":"web","upstream":"shop-web-1:3000"}],
		"connector":"box.example.com"}`)

	want := switchboard.Forward{Upstream: "unix/" + switchboard.ConnectorSocket, Strip: switchboard.ConnectorPath}
	for _, path := range []string{switchboard.ConnectorPath, switchboard.ConnectorPath + "/", switchboard.ConnectorPath + "/connector.v1.Box/Describe"} {
		if got := forwarded(t, table, "box.example.com", path); got != want {
			t.Errorf("%s forwards to %+v, want %+v", path, got, want)
		}
	}
	for _, path := range []string{"/", switchboard.ConnectorPath + "x"} {
		if got := forwarded(t, table, "box.example.com", path); got.Upstream != "shop-web-1:3000" {
			t.Errorf("%s forwards to %+v, want the app that claims the hostname", path, got)
		}
	}
	if forward, ok := table.Forward("shop.example.com", switchboard.ConnectorPath); ok {
		t.Errorf("an unclaimed hostname forwards the connector path to %+v, want it refused: only the connector's hostname reaches it", forward)
	}
}

func TestAnUnclaimedHostnameThePreviewEntryAndItsProbeAreAllRefused(t *testing.T) {
	t.Parallel()

	table := mustRead(t, `{"grace":"30s",
		"claims":[{"owner":"ocel--shop--pr-1","hostname":"web--pr-1.preview.example.com","pointer":"@pr-1"}],
		"routes":[{"owner":"ocel--shop--pr-1","pointer":"@pr-1","app":"web","upstream":"shop-web-pr-1:3000"}],
		"preview":"preview.example.com"}`)

	forwarded(t, table, "web--pr-1.preview.example.com", "/")
	for _, host := range []string{
		"blog.example.com", "other.preview.example.com", "a.b.preview.example.com",
		"ocel-edge-probe.preview.example.com", "preview.example.com", "",
	} {
		if forward, ok := table.Forward(host, "/"); ok {
			t.Errorf("%q forwards to %+v, want it refused", host, forward)
		}
	}
}

func TestATableCarryingEveryKindOfRowOcelWritesIsRead(t *testing.T) {
	t.Parallel()

	mustRead(t, `{"grace":"12s",
		"claims":[{"owner":"ocel--shop--production","hostname":"shop.example.com","pointer":"@production"}],
		"routes":[{"owner":"ocel--shop--production","pointer":"@production","app":"web","upstream":"shop-web-1:3000"}],
		"pins":[{"hostname":"shop.example.com","path":"/var/lib/ocel/certs/shop"}],
		"preview":"preview.example.com",
		"connector":"box.example.com"}`)
}

func TestATableOcelCouldNotHaveWrittenIsRefusedWhole(t *testing.T) {
	t.Parallel()

	const web = `{"owner":"ocel--shop--production","pointer":"@production","app":"web","upstream":"shop-web-1:3000"}`
	for what, document := range map[string]string{
		"a field ocel never writes":           `{"grace":"30s","upstreams":[]}`,
		"a grace that is not a duration":      `{"grace":"soon"}`,
		"no grace at all":                     `{}`,
		"a claim naming no pointer":           `{"grace":"30s","claims":[{"owner":"o","hostname":"a.example.com","pointer":""}]}`,
		"a claim naming no owner":             `{"grace":"30s","claims":[{"owner":"","hostname":"a.example.com","pointer":"p"}]}`,
		"a claim naming no hostname":          `{"grace":"30s","claims":[{"owner":"o","hostname":"","pointer":"p"}]}`,
		"a wildcard claim":                    `{"grace":"30s","claims":[{"owner":"o","hostname":"*.example.com","pointer":"p"}]}`,
		"a claim holding the separator":       `{"grace":"30s","claims":[{"owner":"o/x","hostname":"a.example.com","pointer":"p"}]}`,
		"a claimed app holding the separator": `{"grace":"30s","claims":[{"owner":"o","hostname":"a.example.com","pointer":"p","app":"w/x"}]}`,
		"a route naming no app":               `{"grace":"30s","routes":[{"owner":"o","pointer":"p","app":"","upstream":"a:1"}]}`,
		"a route holding the separator":       `{"grace":"30s","routes":[{"owner":"o","pointer":"p/x","app":"web","upstream":"a:1"}]}`,
		"a route naming no upstream":          `{"grace":"30s","routes":[{"owner":"o","pointer":"p","app":"web","upstream":""}]}`,
		"a route whose upstream has no port":  `{"grace":"30s","routes":[{"owner":"o","pointer":"p","app":"web","upstream":"shop-web-1"}]}`,
		"a route whose port is not a port":    `{"grace":"30s","routes":[{"owner":"o","pointer":"p","app":"web","upstream":"shop-web-1:99999"}]}`,
		"a route naming a health path":        `{"grace":"30s","routes":[{"owner":"o","pointer":"p","app":"web","upstream":"a:1","health":"/up"}]}`,
		"a preview base of one label":         `{"grace":"30s","routes":[` + web + `],"preview":"localhost"}`,
		"a preview base no dns label spells":  `{"grace":"30s","preview":"pre_view.example.com"}`,
		"two tables":                          `{"grace":"30s"}{"grace":"30s"}`,
	} {
		if _, err := switchboard.Read([]byte(document)); err == nil {
			t.Errorf("a table with %s was read, want it refused: %s", what, document)
		}
	}
}

func TestAHostnameTwoRoutesWouldBothAnswerIsRefused(t *testing.T) {
	t.Parallel()

	_, err := switchboard.Read([]byte(`{"grace":"30s",
		"claims":[
			{"owner":"ocel--shop--production","hostname":"shop.example.com","pointer":"@production"},
			{"owner":"ocel--shop--production","hostname":"shop.example.com","pointer":"@production","app":"web"}],
		"routes":[{"owner":"ocel--shop--production","pointer":"@production","app":"web","upstream":"shop-web-1:3000"}]}`))
	if err == nil || !strings.Contains(err.Error(), "forwarded by both") {
		t.Fatalf("Read() = %v, want a hostname claimed twice refused as forwarded by both", err)
	}
}
