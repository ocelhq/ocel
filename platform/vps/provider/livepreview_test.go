package vps_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

const livePreviewBase = "preview.ocel.home.arpa"

func (vm machine) loadedProxyConfig(t *testing.T) map[string]any {
	t.Helper()

	var read map[string]any
	if err := json.Unmarshal([]byte(vm.ssh(t, "sudo cat "+quote(host.ProxyConfig))), &read); err != nil {
		t.Fatalf("read what the proxy loaded: %v", err)
	}
	return read
}

func (vm machine) asksFor(t *testing.T, hostname string) int {
	t.Helper()

	said := vm.peers(t, "wget -q -S -O /dev/null --header="+quote("Host: "+hostname)+
		" http://"+caddy.Container+"/ 2>&1 | grep -m1 'HTTP/' || true")
	for _, field := range strings.Fields(said) {
		if status, err := strconv.Atoi(field); err == nil && status >= 100 && status < 600 {
			return status
		}
	}
	t.Fatalf("the box answered %q for %s, and no status could be read out of it", said, hostname)
	return 0
}

func previewEntryOn(t *testing.T, vm machine) edge.Edge {
	t.Helper()

	p := vm.deploying(t)
	t.Cleanup(func() { closing(t, p) })
	front, err := p.Edges().Open(boxedge.Kind)
	if err != nil {
		t.Fatalf("Open(%q) = %v", boxedge.Kind, err)
	}
	if _, err := front.ReconcilePreviewWildcard(context.Background(), edge.PreviewWildcardSpec{
		BaseDomain: livePreviewBase,
		GrammarMin: edge.PreviewGrammarMin,
		GrammarMax: edge.PreviewGrammarMax,
	}); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	t.Cleanup(func() {
		if err := front.DestroyPreviewWildcard(context.Background(), livePreviewBase); err != nil {
			t.Errorf("DestroyPreviewWildcard: %v", err)
		}
	})
	return front
}

func TestLiveThePreviewProbeIsOrderedOnItsFirstHandshakeAndTheWildcardNever(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	spoken := vm.proxyLogBytes(t)
	previewEntryOn(t, vm)
	wildcard := edge.PreviewWildcard(livePreviewBase)
	probe := edge.ProbeHostname(wildcard)

	if written, err := json.Marshal(vm.loadedProxyConfig(t)); err != nil || strings.Contains(string(written), wildcard) {
		t.Errorf("the loaded configuration names %s (%v), and a config naming a hostname is reloaded the day it changes:\n%s", wildcard, err, written)
	}
	vm.handshakes(t, probe)

	logs := vm.proxyLogSince(t, spoken)
	if !strings.Contains(logs, probe) {
		t.Fatalf("the proxy said nothing about %s after a handshake asked for it, so this window contains no order to read an absence out of:\n%s", probe, logs)
	}
	if strings.Contains(logs, wildcard) {
		t.Errorf("the proxy names %s in what it logged since the entry was installed:\n%s\nA wildcard subject needs dns-01 at every ca, so an order for it fails on every attempt.", wildcard, logs)
	}
}

func TestLiveTheLoadedConfigurationOrdersOnDemandOnlyWhatTheSwitchboardAdmits(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	previewEntryOn(t, vm)

	loaded := vm.loadedProxyConfig(t)
	onDemand, _ := nestedIn(t, loaded, "apps", "tls", "automation", "on_demand").(map[string]any)
	permission, _ := onDemand["permission"].(map[string]any)
	endpoint := caddy.PermissionEndpoint(host.SwitchboardPermission.Path)
	if len(onDemand) != 1 || permission["module"] != "http" || permission["endpoint"] != endpoint {
		t.Fatalf("the loaded configuration orders on demand through %v, want the switchboard's %s alone: an on-demand policy nobody guards is an acme trigger a stranger drives with a junk subdomain until this box is locked out of its own tls", onDemand, endpoint)
	}
	relayed, _ := json.Marshal(nestedIn(t, loaded, "apps", "http", "servers", "admit", "routes"))
	if !strings.Contains(string(relayed), `"dial":"`+host.SwitchboardPermission.Dial+`"`) {
		t.Fatalf("the loaded configuration relays %s to %s, want the switchboard's admission at %s", endpoint, relayed, host.SwitchboardPermission.Dial)
	}
	if status := vm.asksFor(t, "pr-7."+livePreviewBase); status != http.StatusNotFound {
		t.Errorf("an unclaimed preview hostname was answered %d, want the switchboard's 404", status)
	}
}

func TestLiveEveryHostnameNothingClaimsUnderOrBesideTheBaseIsTheBoxsOwnRefusal(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	previewEntryOn(t, vm)
	for what, hostname := range map[string]string{
		"one label under the base":               "pr-7." + livePreviewBase,
		"one label containing the app separator": "shop--pr-7--web." + livePreviewBase,
		"two labels under the base":              "pr-7.api." + livePreviewBase,
		"the base itself":                        livePreviewBase,
		"a hostname outside the base":            "pr-7.preview.example.invalid",
	} {
		if status := vm.asksFor(t, hostname); status != http.StatusNotFound {
			t.Errorf("%s (%s) was answered %d, want the switchboard's 404: a hostname nothing on this box claims is told nothing about it", what, hostname, status)
		}
	}
}

func TestLiveTheWildcardIsOwnedByThePreviewEntryOnlyWhileItsRouteExists(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	front := previewEntryOn(t, vm)
	ctx := context.Background()
	wildcard := edge.PreviewWildcard(livePreviewBase)

	owner, err := front.DomainOwner(ctx, wildcard)
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if owner != edge.PreviewEntryOwner {
		t.Fatalf("DomainOwner(%s) = %q, want %q", wildcard, owner, edge.PreviewEntryOwner)
	}
	if err := front.DestroyPreviewWildcard(ctx, livePreviewBase); err != nil {
		t.Fatalf("DestroyPreviewWildcard: %v", err)
	}
	if owner, err := front.DomainOwner(ctx, wildcard); err != nil || owner != "" {
		t.Errorf("DomainOwner(%s) = %q, %v once the route is gone, want nobody", wildcard, owner, err)
	}
}

func nestedIn(t *testing.T, read map[string]any, path ...string) any {
	t.Helper()

	var at any = read
	for _, step := range path {
		object, ok := at.(map[string]any)
		if !ok {
			t.Fatalf("the loaded configuration has nothing at %s", strings.Join(path, "."))
		}
		at = object[step]
	}
	return at
}
