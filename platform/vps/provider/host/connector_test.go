package host

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func connectorConfig() []byte {
	return []byte(`{"console":"https://ocel.app","connectorId":"con_1","organizationId":"org_1"}`)
}

func TestTheUnitIsWrittenAfterEverythingItWatches(t *testing.T) {
	t.Parallel()

	items := ConnectorItems([]byte("#!/bin/sh\n"), connectorConfig())
	at := slices.IndexFunc(items, func(item Item) bool { return item.Kind == KindUnit })
	if at < 0 {
		t.Fatal("nothing stands the connector up, so a reboot leaves the console dialling a socket nobody is listening on")
	}
	unit := items[at]
	for _, watched := range unit.Watch {
		written := slices.IndexFunc(items, func(item Item) bool { return item.Kind == KindFile && item.Name == watched })
		if written < 0 {
			t.Errorf("the unit watches %s and nothing writes it", watched)
			continue
		}
		if written > at {
			t.Errorf("%s is written after the unit that watches it, so the restart runs against what was there before", watched)
		}
	}
}

func TestTheUnitIsRestartedWhenTheBinaryOrTheConfigChanges(t *testing.T) {
	t.Parallel()

	unitOf := func(items []Item) Item {
		at := slices.IndexFunc(items, func(item Item) bool { return item.Kind == KindUnit })
		return items[at]
	}
	held := unitOf(ConnectorItems([]byte("one"), connectorConfig()))
	for name, items := range map[string][]Item{
		"another binary": ConnectorItems([]byte("two"), connectorConfig()),
		"another config": ConnectorItems([]byte("one"), []byte(`{"console":"https://elsewhere.example"}`)),
	} {
		if unitOf(items).Digest() == held.Digest() {
			t.Errorf("%s leaves the unit item current, so nothing restarts the process serving the old one", name)
		}
	}
	if unitOf(ConnectorItems([]byte("one"), connectorConfig())).Digest() != held.Digest() {
		t.Error("the same binary and config draw a different unit item, so every run restarts the connector")
	}
}

func TestTheUnitRunsUnelevatedOverASocketAndComesBackOnABoot(t *testing.T) {
	t.Parallel()

	written := string(connectorUnit())
	for _, want := range []string{
		"User=" + deployUser,
		"ExecStart=" + ConnectorBinary + " --config " + ConnectorConfig + " --listen unix://" + ConnectorSocket,
		"Restart=on-failure",
		"WantedBy=multi-user.target",
		"After=docker.service",
	} {
		if !strings.Contains(written, want) {
			t.Errorf("the unit carries no %q:\n%s", want, written)
		}
	}
	if strings.Contains(written, "RuntimeDirectory") {
		t.Errorf("the unit lets systemd own %s, which the proxy has bind-mounted: a stop would take the directory the container still holds:\n%s", ConnectorRun, written)
	}
}

func TestTheConfigTheConnectorReadsNamesTheKeyOnlyTheBoxHolds(t *testing.T) {
	t.Parallel()

	written, err := keyPathed(connectorConfig())
	if err != nil {
		t.Fatalf("keyPathed: %v", err)
	}
	var held map[string]any
	if err := json.Unmarshal(written, &held); err != nil {
		t.Fatalf("the config is not an object: %v", err)
	}
	if held["keyPath"] != ConnectorKey {
		t.Errorf("keyPath = %v, want %s: the connector mints its own key and the console only ever sees the public half", held["keyPath"], ConnectorKey)
	}
	if held["console"] != "https://ocel.app" {
		t.Errorf("the install dropped what the console told it: %v", held)
	}
	if _, err := keyPathed([]byte("[]")); err == nil {
		t.Error("keyPathed() took a config that is no object")
	}
}

func TestTheConnectorConfigAndKeyAreReadableByNobodyElse(t *testing.T) {
	t.Parallel()

	items := ConnectorItems([]byte("#!/bin/sh\n"), connectorConfig())
	for _, tc := range []struct {
		kind, name, owner string
		mode              os.FileMode
	}{
		{KindDir, connectorRoot, stateOwner, 0o700},
		{KindFile, ConnectorConfig, stateOwner, 0o600},
		{KindFile, ConnectorBinary, rootOwner, 0o755},
	} {
		at := slices.IndexFunc(items, func(item Item) bool { return item.Kind == tc.kind && item.Name == tc.name })
		if at < 0 {
			t.Errorf("nothing writes %s %s", tc.kind, tc.name)
			continue
		}
		if items[at].Owner != tc.owner || items[at].Mode != tc.mode {
			t.Errorf("%s is written as %s at %04o, want %s at %04o", tc.name, items[at].Owner, items[at].Mode, tc.owner, tc.mode)
		}
	}
}

func TestTheProbeReadsAWatchedUnitExactlyAsTheItemStatesIt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	binary, config, unit := write("connector", "#!/bin/sh\n"), write("config.json", string(connectorConfig())), write("unit", string(connectorUnit()))

	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, "systemctl"), []byte(`#!/bin/sh
case "$1" in
cat) exit 0 ;;
is-active) printf 'active\n' ;;
is-enabled) printf 'enabled\n' ;;
*) exit 1 ;;
esac`), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"sha256sum", "cut"} {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(found, filepath.Join(path, tool)); err != nil {
			t.Fatal(err)
		}
	}

	stated := Item{
		Kind:    KindUnit,
		Name:    ConnectorUnit,
		Owner:   rootOwner,
		Content: unitWatchFacts(connectorUnit(), []byte("#!/bin/sh\n"), connectorConfig()),
		Watch:   []string{unit, binary, config},
	}
	cmd := exec.Command("/bin/sh", "-c", stated.probe())
	cmd.Env = []string{"PATH=" + path}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	rendered, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe the unit: %v\n%s", err, stderr.String())
	}
	observed, _, err := readSurvey(string(rendered))
	if err != nil {
		t.Fatal(err)
	}
	if observed[stated.ID()] != stated.Digest() {
		t.Errorf("the probe reads the unit as %q and the item states %q, so every re-run restarts a connector that stands",
			observed[stated.ID()], stated.Digest())
	}
}

func TestTakingTheConnectorOffLeavesNoUnitBinaryOrKey(t *testing.T) {
	t.Parallel()

	written := connectorRemoval()
	for _, want := range []string{connectorUnitFile, ConnectorBinary, connectorRoot, connectorTmpfiles, ConnectorSocket} {
		if !strings.Contains(written, quoted(want)) {
			t.Errorf("the removal leaves %s behind:\n%s", want, written)
		}
	}
	if !strings.Contains(written, "systemctl disable --now") {
		t.Errorf("the removal leaves the unit running:\n%s", written)
	}
	if !strings.Contains(written, "daemon-reload") {
		t.Errorf("the removal leaves systemd holding a unit whose file is gone:\n%s", written)
	}
}

func TestWhatTheConnectorSaysAboutItselfIsReadBack(t *testing.T) {
	t.Parallel()

	held, err := readConnectorStanding("version=0.4.1\nkey=ZmFrZQ==\n")
	if err != nil {
		t.Fatalf("readConnectorStanding: %v", err)
	}
	if !held.Installed || held.Version != "0.4.1" || held.PublicKey != "ZmFrZQ==" {
		t.Errorf("standing = %+v, want the version and key the box printed", held)
	}
	absent, err := readConnectorStanding("\n")
	if err != nil || absent.Installed {
		t.Errorf("standing = %+v, %v, want nothing installed", absent, err)
	}
	if _, err := readConnectorStanding("version=0.4.1\nkey=\n"); err == nil {
		t.Error("a connector holding no key was reported as paired, and the console can verify no heartbeat against it")
	}
}

func TestTheProxyForwardsTheConnectorPathAheadOfEverySurface(t *testing.T) {
	t.Parallel()

	state := ProxyState{
		Grace:     DeployWindow,
		Connector: true,
		Claims:    []HostClaim{{Hostname: "box.example.com", Owner: surface, Pointer: pointed, App: "web"}},
		Routes:    []AppRoute{{RouteKey: keyed("web"), Upstream: "web:3000"}},
	}
	rendered := mustRender(t, state)

	var read caddyConfig
	if err := json.Unmarshal(rendered, &read); err != nil {
		t.Fatal(err)
	}
	routes := read.Apps.HTTP.Servers[proxyServer].Routes
	if len(routes) == 0 || routes[0].Identity != connectorRoute {
		t.Fatalf("the routes lead with %v, want the connector: a reverse_proxy handler is terminal, so a surface claiming this box's hostname would answer the console instead",
			identities(routes))
	}
	first := routes[0]
	if len(first.Match) != 1 || first.Match[0].Host != nil {
		t.Errorf("the connector route matches %v, want a path on whatever hostname the box answers to", first.Match)
	}
	if !slices.Equal(first.Match[0].Path, []string{ConnectorPath, ConnectorPath + "/*"}) {
		t.Errorf("the connector route matches paths %v, want %s and everything under it", first.Match[0].Path, ConnectorPath)
	}
	stripped := slices.IndexFunc(first.Handle, func(handled caddyForward) bool { return handled.Handler == rewriteHandler })
	if stripped < 0 || first.Handle[stripped].StripPathPrefix != ConnectorPath {
		t.Errorf("the connector route forwards %s unstripped, so the connector is asked for a procedure under a prefix it serves nothing at", ConnectorPath)
	}
	dialled := first.Handle[len(first.Handle)-1]
	if dialled.Handler != forwardHandler || len(dialled.Upstreams) != 1 || dialled.Upstreams[0].Dial != ConnectorDial {
		t.Errorf("the connector route dials %v, want %s", dialled.Upstreams, ConnectorDial)
	}

	held, err := ReadProxyState(rendered)
	if err != nil {
		t.Fatalf("ReadProxyState: %v", err)
	}
	if !held.Connector {
		t.Error("the route the render wrote reads back as one ocel did not write, so the next deploy takes it with it")
	}
}

func TestABoxWithNoConnectorForwardsNothingToOne(t *testing.T) {
	t.Parallel()

	rendered := mustRender(t, ProxyState{Grace: DeployWindow})
	if strings.Contains(string(rendered), ConnectorPath) {
		t.Errorf("a box nothing was added to still routes %s:\n%s", ConnectorPath, rendered)
	}
	held, err := ReadProxyState(rendered)
	if err != nil {
		t.Fatalf("ReadProxyState: %v", err)
	}
	if held.Connector {
		t.Error("a config carrying no connector route read back as one that does")
	}
}

func TestASurfaceThatClaimsNothingStillMatchesNothing(t *testing.T) {
	t.Parallel()

	rendered := mustRender(t, ProxyState{
		Grace:  DeployWindow,
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: "web:3000"}},
	})
	var read caddyConfig
	if err := json.Unmarshal(rendered, &read); err != nil {
		t.Fatal(err)
	}
	for _, route := range read.Apps.HTTP.Servers[proxyServer].Routes {
		if route.Identity == boxIdentity {
			continue
		}
		if len(route.Match) == 1 && route.Match[0].Host != nil && len(*route.Match[0].Host) == 0 && len(route.Match[0].Path) == 0 {
			continue
		}
		if len(route.Match) == 0 {
			t.Errorf("%s matches everything, and an app claiming no hostname is a route nothing should reach", route.Identity)
		}
	}
}

func identities(routes []caddyRoute) []string {
	named := make([]string, 0, len(routes))
	for _, route := range routes {
		named = append(named, route.Identity)
	}
	return named
}
