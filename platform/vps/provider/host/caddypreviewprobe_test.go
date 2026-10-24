//go:build integration

package host

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

const internalBase = "preview.ocel.home.arpa"

func TestAnUnclaimedHostnameUnderThePreviewBaseIsToldNothingAboutTheBox(t *testing.T) {
	ask := probingConfig(t, previewing(), issuedByNobody(t, mustRender(t, previewing())))

	for what, hostname := range map[string]string{
		"a preview hostname nothing claims": "pr-7." + previewBase,
		"the probe hostname":                edge.ProbeHostname(edge.PreviewWildcard(previewBase)),
		"a hostname outside the base":       "pr-7.preview.example.org",
	} {
		said := ask(hostname)
		if said.status != http.StatusNotFound {
			t.Errorf("%s (%s) answers %d, want 404", what, hostname, said.status)
		}
		if said.body != "" {
			t.Errorf("%s (%s) answers with a body of %q, want nothing: anyone who resolves a name under the base reaches this, and it names no project, no box and no other preview",
				what, hostname, said.body)
		}
		if said.router != boxapi.SwitchboardRouter {
			t.Errorf("%s (%s) answers with %s: %q, want %q", what, hostname, router.HeaderRouter, said.router, boxapi.SwitchboardRouter)
		}
	}
}

func TestARealProxyOrdersForThePreviewProbeAndNeverForTheWildcardOrAnUnclaimedPreview(t *testing.T) {
	rig, _ := probedBox(t, previewing(), issuedByNobody(t, mustRender(t, previewing())))

	wildcard := edge.PreviewWildcard(previewBase)
	probe := edge.ProbeHostname(wildcard)
	unclaimed := "pr-7." + previewBase
	rig.handshake(unclaimed)
	rig.handshake(probe)
	logs := rig.ordered(t, probe)
	if managed(logs, onDemandOrder, unclaimed) {
		t.Errorf("the proxy ordered for %s, a preview hostname nothing claims: the wildcard record sends every name under the base here, and each one would be an order against the base's allowance:\n%s", unclaimed, logs)
	}
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, wildcard) {
			t.Errorf("the proxy names %s in its own log:\n%s\nthe whole line: %s\nA wildcard subject needs dns-01 at every ca and this box has no dns writer, so an order for it fails on every attempt.",
				wildcard, logs, line)
		}
	}
	if strings.Contains(logs, acmeDirectory) {
		t.Errorf("the proxy reached a public CA from a package-level `go test`:\n%s", logs)
	}
}

func TestAPreviewBaseNoPublicCaWillIssueForIsManagedInternallyAndReachesNoCaAtAll(t *testing.T) {
	state := routed()
	state.PreviewBase = internalBase
	rig, _ := probedBox(t, state, issuedByNobody(t, mustRender(t, state)))

	probe := edge.ProbeHostname(edge.PreviewWildcard(internalBase))
	rig.handshake(probe)
	const obtained = "certificate obtained successfully"
	var logs string
	for range 100 {
		if logs = logsOf(rig.name); managed(logs, obtained, probe) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !managed(logs, obtained, probe) {
		t.Fatalf("the proxy has no certificate for %s after a handshake asked for it, so nothing here was decided after the issuance decision and every absence below is read off a window that never reached one:\n%s", probe, logs)
	}
	if strings.Contains(logs, unreachableCA) {
		t.Errorf("a hostname under %s was taken to an acme issuer rather than caddy's internal one: what keeps this package off a public CA is that a name no public CA will issue for is managed internally, and this config points the catch-all at a directory that answers nothing precisely so the wrong answer costs a failed order rather than a real one:\n%s",
			internalBase, logs)
	}
	if strings.Contains(logs, acmeDirectory) {
		t.Errorf("a preview base under %s reached a public CA, so the live tier cannot use one to install a preview entry without putting a real order on the wire from CI:\n%s",
			internalBase, logs)
	}
	if strings.Contains(logs, edge.PreviewWildcard(internalBase)) {
		t.Errorf("the proxy names the wildcard in its own log:\n%s", logs)
	}
}
