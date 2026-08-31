package vps_test

import (
	"context"
	"encoding/base64"
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

const fellThrough = 410

func tellingTheBoxsOwnRefusalApart(t *testing.T, vm machine) {
	t.Helper()

	before := vm.ssh(t, "sudo cat "+quote(host.ProxyConfig))
	loads := func(document string) {
		vm.ssh(t, "printf %s "+quote(base64.StdEncoding.EncodeToString([]byte(document)))+
			" | base64 -d | sudo tee "+quote(host.ProxyConfig)+" >/dev/null")
		vm.drives(t, "flip "+quote(host.ProxyConfigMount))
	}

	read := vm.loadedProxyConfig(t)
	routes, held := nestedIn(t, read, "apps", "http", "servers", "ocel", "routes").([]any)
	if !held || len(routes) == 0 {
		t.Fatal("the loaded configuration carries no routes, so there is no box refusal behind the catch-all to tell it apart from")
	}
	behind, held := routes[len(routes)-1].(map[string]any)
	if !held || behind["match"] != nil {
		t.Fatalf("the last route this box loaded is %v, and the route the catch-all has to be told apart from is the matcher-less refusal standing behind every route ocel writes", behind)
	}
	answering, held := behind["handle"].([]any)
	if !held || len(answering) != 1 {
		t.Fatalf("the box's own refusal handles %v, want the one static response every unclaimed hostname reaches", behind["handle"])
	}
	refusal, held := answering[0].(map[string]any)
	if !held {
		t.Fatalf("the box's own refusal handles %v", answering[0])
	}
	refusal["status_code"] = fellThrough

	written, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { loads(before) })
	loads(string(written))
}

func TestLiveTheCatchAllAnswersOneLabelUnderTheBaseAndNothingDeeper(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	previewEntryOn(t, vm)
	tellingTheBoxsOwnRefusalApart(t, vm)

	for _, reached := range []struct {
		what     string
		hostname string
		caught   bool
	}{
		{"one label under the base", "pr-7." + livePreviewBase, true},
		{"one label carrying the app separator", "shop--pr-7--web." + livePreviewBase, true},
		{"two labels under the base", "pr-7.api." + livePreviewBase, false},
		{"the base itself", livePreviewBase, false},
		{"a hostname outside the base", "pr-7.preview.example.invalid", false},
	} {
		status := vm.asksFor(t, reached.hostname)
		switch {
		case status != http.StatusNotFound && status != fellThrough:
			t.Errorf("%s (%s) was answered %d, and this box answers every hostname either off the preview catch-all or off its own refusal behind it", reached.what, reached.hostname, status)
		case (status == http.StatusNotFound) != reached.caught:
			t.Errorf("%s (%s) was answered %d, and the catch-all %s it: caddy's host matcher spends a leading `*.` on exactly one label, which is the whole of what keeps a preview base from swallowing the names beneath it",
				reached.what, reached.hostname, status,
				map[bool]string{true: "did not catch", false: "caught"}[reached.caught])
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
