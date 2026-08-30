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
		" http://"+host.ProxyContainer+"/ 2>&1 | grep -m1 'HTTP/' || true")
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

func TestLiveThePreviewCatchAllIsInstalledAndOrdersNoWildcardCertificate(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	spoken := vm.proxyLogBytes(t)
	previewEntryOn(t, vm)
	wildcard := edge.PreviewWildcard(livePreviewBase)
	probe := edge.ProbeHostname(wildcard)

	server, held := nestedIn(t, vm.loadedProxyConfig(t), "apps", "http", "servers", "ocel").(map[string]any)
	if !held {
		t.Fatalf("the loaded configuration carries no ocel server")
	}
	automatic, held := server["automatic_https"].(map[string]any)
	if !held {
		t.Fatalf("the loaded configuration declares no automatic_https block, so %s is a subject caddy orders a wildcard certificate for and no dns-01 module on this box can answer that challenge", wildcard)
	}
	if skipped, _ := json.Marshal(automatic["skip_certificates"]); string(skipped) != `["`+wildcard+`"]` {
		t.Errorf("the loaded configuration skips certificates for %s, want exactly [%s]", skipped, wildcard)
	}

	logs := vm.proxyLogSince(t, spoken)
	if !strings.Contains(logs, probe) {
		t.Fatalf("the proxy said nothing about %s since the entry was installed, so this window carries no subject collection to read an absence out of:\n%s", probe, logs)
	}
	if strings.Contains(logs, wildcard) {
		t.Errorf("the proxy names %s in what it logged since the entry was installed:\n%s\nA wildcard subject needs dns-01 at every ca, so the order behind that line fails and is retried for as long as the box stands.", wildcard, logs)
	}
}

func TestLiveTheLoadedConfigurationDeclaresNoOnDemandTlsPolicy(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	previewEntryOn(t, vm)

	written, err := json.Marshal(vm.loadedProxyConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "on_demand") {
		t.Fatalf("the loaded configuration declares an on-demand tls policy:\n%s\nCaddy only warns when one carries no permission module and serves anyway, so a catch-all beside it is an unauthenticated acme trigger a stranger drives with a junk subdomain until this box is locked out of its own tls.", written)
	}
	if !strings.Contains(string(written), edge.PreviewWildcard(livePreviewBase)) {
		t.Fatalf("the loaded configuration names no preview entry at all, so the absence above is the absence of the whole feature:\n%s", written)
	}
}

func TestLiveTheCatchAllAnswersOneLabelUnderTheBaseAndNothingDeeper(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	previewEntryOn(t, vm)

	for what, hostname := range map[string]string{
		"one label under the base":    "pr-7." + livePreviewBase,
		"two labels under the base":   "pr-7.api." + livePreviewBase,
		"a hostname outside the base": "pr-7.preview.example.invalid",
	} {
		if status := vm.asksFor(t, hostname); status != http.StatusNotFound {
			t.Errorf("%s (%s) was answered %d, want the bare 404 this box gives a hostname nothing on it claims", what, hostname, status)
		}
	}
}

func TestLiveTheWildcardIsOwnedByThePreviewEntryOnlyWhileItsRouteStands(t *testing.T) {
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

	var held any = read
	for _, step := range path {
		carried, ok := held.(map[string]any)
		if !ok {
			t.Fatalf("the loaded configuration carries nothing at %s", strings.Join(path, "."))
		}
		held = carried[step]
	}
	return held
}
