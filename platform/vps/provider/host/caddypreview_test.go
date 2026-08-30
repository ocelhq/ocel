package host

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const previewBase = "preview.example.com"

func previewing() ProxyState {
	state := routed()
	state.PreviewBase = previewBase
	return state
}

func skipped(t *testing.T, read map[string]any) []string {
	t.Helper()

	server, held := servers(t, read)[proxyServer].(map[string]any)
	if !held {
		t.Fatalf("the rendered config carries no %s server", proxyServer)
	}
	automatic, held := server["automatic_https"].(map[string]any)
	if !held {
		return nil
	}
	var names []string
	for _, name := range automatic["skip_certificates"].([]any) {
		names = append(names, name.(string))
	}
	return names
}

func TestThePreviewCatchAllIsTheOneRouteTheBoxKeepsOutOfTheAcmeSubjectCollection(t *testing.T) {
	t.Parallel()

	wildcard := edge.PreviewWildcard(previewBase)
	names := skipped(t, loading(t, previewing()))
	if !slices.Equal(names, []string{wildcard}) {
		t.Fatalf("the config skips certificates for %v, want exactly [%s]: %s enters caddy's automatic-https subject collection like any other host matcher, and a wildcard order needs a dns-01 module this box has none of, so stock caddy retries an order it can never place for as long as the box stands",
			names, wildcard, wildcard)
	}
	if probe := edge.ProbeHostname(wildcard); slices.Contains(names, probe) {
		t.Errorf("%s is skipped too, and the probe `ocel domain use --preview` waits on reads %s off an https response: a hostname with no certificate has no chain for that client to verify, so skipping it refuses the settle outright",
			probe, edge.HeaderEdge)
	}
}

func TestABoxServingNoPreviewsDeclaresNoSkipAtAll(t *testing.T) {
	t.Parallel()

	if names := skipped(t, loading(t, routed())); len(names) != 0 {
		t.Errorf("a box with no preview entry skips %v, want nothing: the exclusion is bought by the one route that cannot be issued for, and every hostname beside it is a name this box serves and holds a certificate for", names)
	}
}

func TestThePreviewEntryAndItsProbeAreTwoRoutesMatchingOneHostnameEach(t *testing.T) {
	t.Parallel()

	wildcard := edge.PreviewWildcard(previewBase)
	rendered := mustRender(t, previewing())
	var read caddyConfig
	if err := json.Unmarshal(rendered, &read); err != nil {
		t.Fatal(err)
	}
	routes := read.Apps.HTTP.Servers[proxyServer].Routes
	found := map[string][]string{}
	for _, route := range routes {
		if len(route.Match) == 1 {
			found[route.Identity] = route.Match[0].Host
		}
	}
	for identity, want := range map[string]string{
		previewEntryIdentity(previewBase): wildcard,
		previewProbeIdentity(previewBase): edge.ProbeHostname(wildcard),
	} {
		if !slices.Equal(found[identity], []string{want}) {
			t.Errorf("%s matches %v, want exactly [%s]", identity, found[identity], want)
		}
	}
	at := slices.IndexFunc(routes, func(route caddyRoute) bool {
		return route.Identity == previewEntryIdentity(previewBase)
	})
	if at < 0 || at+1 >= len(routes) || routes[at+1].Identity != boxIdentity {
		t.Errorf("the preview catch-all does not stand immediately ahead of %s: it answers the hostnames under the base that no preview claims, and every route ocel writes for one of them is written ahead of it", boxIdentity)
	}
}

func TestThePreviewCatchAllRendersItsSuffixRatherThanAnEmptyHostMatcher(t *testing.T) {
	t.Parallel()

	for _, base := range []string{"*.preview.example.com", "preview/example.com", "*", "example", "preview.example.com."} {
		if _, err := RenderProxyConfig(ProxyState{Grace: DrainWindow, PreviewBase: base}); err == nil {
			t.Errorf("a preview entry on base %q renders, and a catch-all whose suffix is not a name receives more than one label under one base", base)
		}
	}
	if err := PreviewBaseUsable(""); err == nil {
		t.Error("an empty base is a usable one, and the route it would install carries no host matcher: it receives every hostname pointed at this machine, a mistyped production hostname included")
	}
}

func TestThePreviewEntryReadsBackAsTheBaseThatRenderedIt(t *testing.T) {
	t.Parallel()

	read, err := ReadProxyState(mustRender(t, previewing()))
	if err != nil {
		t.Fatalf("ReadProxyState() = %v", err)
	}
	if read.PreviewBase != previewBase {
		t.Fatalf("the preview base reads back as %q, want %q: this file is the only thing that answers whether the shared preview entry stands", read.PreviewBase, previewBase)
	}
	if !slices.Equal(read.Routes, previewing().Routes) || !slices.Equal(read.Claims, previewing().Claims) {
		t.Errorf("the preview entry cost the box its routes (%v) or its claims (%v)", read.Routes, read.Claims)
	}
}

func TestAConfigDeclaringOnDemandTlsIsRefusedRatherThanServed(t *testing.T) {
	t.Parallel()

	rendered := mustRender(t, previewing())
	var config map[string]any
	if err := json.Unmarshal(rendered, &config); err != nil {
		t.Fatal(err)
	}
	apps := config["apps"].(map[string]any)
	apps["tls"] = map[string]any{"automation": map[string]any{
		"on_demand": map[string]any{"ask": "http://127.0.0.1:9/ask"},
	}}
	written, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProxyState(written); err == nil {
		t.Fatal("a proxy configuration declaring on-demand tls reads back as one ocel wrote: caddy only warns when on-demand carries no permission module and serves anyway, so a catch-all beside it is an unauthenticated acme trigger a stranger drives with a junk subdomain until this box is locked out of its own tls")
	}
}

func TestTheRendererHasNoWayToDeclareOnDemandTlsAtAll(t *testing.T) {
	t.Parallel()

	for what, state := range map[string]ProxyState{
		"a box serving previews": previewing(),
		"a box serving one app":  routed(),
		"a box serving nothing":  {Grace: DrainWindow},
	} {
		if written := string(mustRender(t, state)); strings.Contains(written, "on_demand") {
			t.Errorf("%s renders a configuration naming on-demand tls:\n%s", what, written)
		}
	}
}

func TestAnUnclaimedHostnameUnderThePreviewBaseIsToldNothingAboutTheBox(t *testing.T) {
	ask := probingConfig(t, issuedByNobody(t, mustRender(t, previewing())))

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
		if said.edge != EdgeName {
			t.Errorf("%s (%s) answers with %s: %q, want %q", what, hostname, EdgeHeader, said.edge, EdgeName)
		}
	}
}

const fellThrough = http.StatusGone

func tellingTheFallthroughApart(t *testing.T, rendered []byte) []byte {
	t.Helper()

	var read caddyConfig
	if err := json.Unmarshal(rendered, &read); err != nil {
		t.Fatal(err)
	}
	server := read.Apps.HTTP.Servers[proxyServer]
	at := slices.IndexFunc(server.Routes, func(route caddyRoute) bool { return route.Identity == boxIdentity })
	if at < 0 {
		t.Fatalf("the rendered config carries no %s route to tell the catch-all apart from", boxIdentity)
	}
	server.Routes[at].Handle[0].Status = fellThrough
	read.Apps.HTTP.Servers[proxyServer] = server
	written, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	return written
}

func TestARealProxyMatchesTheCatchAllOneLabelUnderTheBaseAndNoDeeper(t *testing.T) {
	ask := probingConfig(t, issuedByNobody(t, tellingTheFallthroughApart(t, mustRender(t, previewing()))))

	for _, reached := range []struct {
		what     string
		hostname string
		caught   bool
	}{
		{"one label under the base", "pr-7." + previewBase, true},
		{"one label carrying the app separator", "shop--pr-7--web." + previewBase, true},
		{"two labels under the base", "pr-7.api." + previewBase, false},
		{"the base itself", previewBase, false},
		{"a base the wildcard is a suffix of", "notpreview.example.com", false},
		{"a hostname outside the base", "pr-7.preview.example.org", false},
	} {
		said := ask(reached.hostname)
		switch {
		case said.status != http.StatusNotFound && said.status != fellThrough:
			t.Errorf("%s (%s) was answered %d, and this config answers every hostname either off the preview catch-all or off the box's own route behind it", reached.what, reached.hostname, said.status)
		case (said.status == http.StatusNotFound) != reached.caught:
			t.Errorf("%s (%s) was answered %d, and the catch-all %s it: caddy's host matcher spends a leading `*.` on exactly one label, which is the whole of what keeps a preview base from swallowing names beneath it",
				reached.what, reached.hostname, said.status,
				map[bool]string{true: "did not catch", false: "caught"}[reached.caught])
		}
		if reached.caught && said.body != "" {
			t.Errorf("%s (%s) answers with a body of %q, want nothing", reached.what, reached.hostname, said.body)
		}
	}
}

func TestARealProxyOrdersForThePreviewProbeAndNeverForTheWildcardBesideIt(t *testing.T) {
	rendered := issuedByNobody(t, mustRender(t, previewing()))
	ask := probingConfig(t, rendered)
	ask("pr-7." + previewBase)

	wildcard := edge.PreviewWildcard(previewBase)
	probe := edge.ProbeHostname(wildcard)
	const managing = "enabling automatic TLS certificate management"

	var logs string
	for range 100 {
		if logs = logsOf(probeName(t)); managed(logs, managing, probe) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !managed(logs, managing, probe) {
		t.Fatalf("the proxy manages certificates for nothing naming %s, so this window carries no subject collection to read an absence out of and every assertion below would hold on a box that never tried:\n%s",
			probe, logs)
	}
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, wildcard) {
			t.Errorf("the proxy names %s in its own log:\n%s\nthe whole line: %s\nA wildcard subject needs dns-01 at every ca and this box has no dns writer, so an order for it fails, is retried, and keeps failing for as long as the box stands — on the one route every box carrying previews has.",
				wildcard, logs, line)
		}
	}
	if strings.Contains(logs, acmeDirectory) {
		t.Errorf("the proxy reached a public CA from a package-level `go test`:\n%s", logs)
	}
}

func TestInstallingThePreviewEntryLoadsItOntoTheRunningProxyAndTakingItDownUnloadsIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stood := claimingBox(t, routed())
	if err := stood.host().InstallPreviewEntry(ctx, previewBase); err != nil {
		t.Fatalf("InstallPreviewEntry() = %v", err)
	}
	held, err := ReadProxyState([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if held.PreviewBase != previewBase {
		t.Fatalf("%s answers previews on %q after the install, want %q", ProxyConfig, held.PreviewBase, previewBase)
	}
	if !slices.ContainsFunc(stood.commands(), func(command string) bool {
		return strings.Contains(command, quoted("flip"))
	}) {
		t.Errorf("the preview entry was written and never loaded, so the running proxy answers no preview hostname at all: %v", stood.commands())
	}

	if err := stood.host().RemovePreviewEntry(ctx, previewBase); err != nil {
		t.Fatalf("RemovePreviewEntry() = %v", err)
	}
	if held, err = ReadProxyState([]byte(stood.held)); err != nil {
		t.Fatal(err)
	}
	if held.PreviewBase != "" {
		t.Errorf("%s still answers previews on %q after the release", ProxyConfig, held.PreviewBase)
	}
}

func TestInstallingThePreviewEntryTwiceWritesTheProxyOnce(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, previewing())
	if err := stood.host().InstallPreviewEntry(context.Background(), previewBase); err != nil {
		t.Fatalf("InstallPreviewEntry() = %v", err)
	}
	for _, command := range stood.commands() {
		if writesProxy(command) || strings.Contains(command, quoted("flip")) {
			t.Errorf("a preview entry already standing rewrote and reloaded the proxy (%q); every reload is a whole-box config post", command)
		}
	}
}

func TestABoxServingOnePreviewBaseRefusesASecondByName(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, previewing())
	err := stood.host().InstallPreviewEntry(context.Background(), "previews.example.org")
	if err == nil {
		t.Fatal("a second preview base was installed over the first, and every live preview on this box is a hostname under the base it was raised on")
	}
	if !strings.Contains(err.Error(), edge.PreviewWildcard(previewBase)) {
		t.Errorf("the refusal reads %q and never names the wildcard already standing", err)
	}
}
