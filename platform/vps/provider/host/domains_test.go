package host

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	claimed = "shop.example.com"
	surface = "ocel--shop--production"
	pointed = "@production"
)

type claimBench struct {
	*bench
	held string
}

func claimingBox(t *testing.T, state ProxyState) *claimBench {
	t.Helper()

	rendered, err := RenderProxyConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	stood := &claimBench{bench: machine(nil), held: string(rendered)}
	stood.answer = servesProxy(stood.bench, &stood.held)
	return stood
}

func routed() ProxyState {
	return ProxyState{
		Grace:  DrainWindow,
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: "shop-web-2222:" + AppPort}},
	}
}

func TestTheDrainWindowAndTheBaselineGracePeriodAreOneNumber(t *testing.T) {
	t.Parallel()

	baseline, err := ReadProxyState(proxyBaseline)
	if err != nil {
		t.Fatalf("ReadProxyState(the baseline) = %v", err)
	}
	if baseline.Grace != DrainWindow {
		t.Errorf("the baseline holds the retired container for %v and a release drains it for %v; the ceiling the user is told and the one the proxy keeps are one number", baseline.Grace, DrainWindow)
	}
}

func TestAClaimedHostnameReadsBackAsTheSurfaceThatClaimedIt(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface}}
	rendered, err := RenderProxyConfig(state)
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}
	read, err := ReadProxyState(rendered)
	if err != nil {
		t.Fatalf("ReadProxyState() = %v", err)
	}
	if !slices.Equal(read.Claims, state.Claims) {
		t.Errorf("the claims read back as %v, want the %v that were written: this file is the only thing that answers which surface claims a hostname", read.Claims, state.Claims)
	}
	if !slices.Equal(read.Routes, state.Routes) {
		t.Errorf("the routes read back as %v, want %v: a claim must not cost the app it sits beside its route", read.Routes, state.Routes)
	}
}

func TestAHostnameClaimForwardsNothingAndIsReachedBeforeTheAppItSitsBeside(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface}}
	rendered, err := RenderProxyConfig(state)
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}

	var read struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Identity string           `json:"@id"`
						Match    []map[string]any `json:"match"`
						Handle   []map[string]any `json:"handle"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(rendered, &read); err != nil {
		t.Fatal(err)
	}
	routes := read.Apps.HTTP.Servers[proxyServer].Routes
	if len(routes) != 3 {
		t.Fatalf("the rendered server carries %d routes, want the claim, the app route and the box's own default: %s", len(routes), rendered)
	}
	if routes[len(routes)-1].Identity != boxIdentity || len(routes[len(routes)-1].Match) != 0 {
		t.Errorf("the last route is %q matching %v, want the box's single unmatched default: every other route ocel writes names the hostnames it answers", routes[len(routes)-1].Identity, routes[len(routes)-1].Match)
	}
	for _, route := range routes[:len(routes)-1] {
		if len(route.Match) == 0 {
			t.Errorf("the route %q matches every hostname this box receives, and the route after it is then dead configuration", route.Identity)
		}
	}
	if !strings.HasPrefix(routes[0].Identity, claimIdentity) {
		t.Errorf("route 0 is %q, want the claim first: the proxy takes the first route that matches, so a claim reached after a catch-all is never reached at all", routes[0].Identity)
	}
	if len(routes[0].Handle) != 0 {
		t.Errorf("the claim carries handlers %v, want none: what a claimed hostname is served by is the domain binding's answer and not this one, and a claim that answers takes the hostname off the app already serving it", routes[0].Handle)
	}
	if len(routes[0].Match) != 1 || !slices.Equal(hosts(t, routes[0].Match[0]), []string{claimed}) {
		t.Errorf("the claim matches %v, want %q alone", routes[0].Match, claimed)
	}
}

func hosts(t *testing.T, match map[string]any) []string {
	t.Helper()
	raw, _ := match["host"].([]any)
	named := make([]string, 0, len(raw))
	for _, value := range raw {
		spelled, _ := value.(string)
		named = append(named, spelled)
	}
	return named
}

func TestARouteNamingASurfaceAndNoHostIsNotOneOcelWrote(t *testing.T) {
	t.Parallel()

	document := strings.Replace(string(mustRender(t, routed())),
		`"@id":"`+keyed("web").identity()+`"`,
		`"@id":"`+claimIdentity+surface+claimSeparator+claimed+`"`, 1)
	if _, err := ReadProxyState([]byte(document)); err == nil {
		t.Error("a claim carrying an app's forwarding handler reads back as a claim, and a deploy that rewrites this file whole would drop what it forwards to")
	}
}

func TestAServerNoDeployWroteIsRefusedTheWayARouteNoDeployWroteIs(t *testing.T) {
	t.Parallel()

	document := strings.Replace(string(mustRender(t, routed())),
		`"servers":{"`+proxyServer+`"`,
		`"servers":{"someone_elses":{"listen":[":8443"],"routes":[]},"`+proxyServer+`"`, 1)
	if !strings.Contains(document, "someone_elses") {
		t.Fatalf("this test never planted the server it is about:\n%s", document)
	}

	_, err := ReadProxyState([]byte(document))
	if err == nil {
		t.Fatal("a server ocel never wrote reads back as nothing at all, and the next deploy renders this file whole from the ocel server alone: whatever else the box was serving is gone with no row naming it")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Errorf("the read failed with %v, want %s: an unwritten route is refused here and an unwritten server is the same file being taken over", err, providerkit.CodeInvalid)
	}
	if !strings.Contains(err.Error(), "someone_elses") {
		t.Errorf("the read is refused with\n%s\nand never names the server it will not rewrite over", err)
	}
}

func TestTheDrainServerADeployWritesReadsBackAsOcelsOwn(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Retiring = "shop-web-1111:" + AppPort
	if _, err := ReadProxyState(mustRender(t, state)); err != nil {
		t.Fatalf("ReadProxyState(a config mid-flip) = %v: the drain server is one this deploy just wrote, and refusing it strands every release between the flip and the steady state", err)
	}
}

func TestASurfaceNamedWithTheSeparatorIsRefusedRatherThanRenderedAmbiguously(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: "ocel" + claimSeparator + "shop"}}
	if _, err := RenderProxyConfig(state); err == nil {
		t.Errorf("a surface named with %q renders a claim that reads back naming a different surface", claimSeparator)
	}
}

func TestAHostnameNamedWithTheSeparatorIsRefusedTheWayASurfaceIs(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: "shop.example.com" + claimSeparator + surface, Owner: surface}}
	if _, err := RenderProxyConfig(state); err == nil {
		t.Errorf("a hostname carrying %q renders a claim whose identity reads back as a different surface and host; the surface half of the same identity is already refused for it", claimSeparator)
	}
}

func TestClaimingAHostnameLoadsItOntoTheRunningProxy(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, routed())
	if err := stood.host().ClaimHost(context.Background(), HostClaim{Hostname: claimed, Owner: surface}); err != nil {
		t.Fatalf("ClaimHost() = %v", err)
	}

	held, err := ReadProxyState([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(held.Claims, []HostClaim{{Hostname: claimed, Owner: surface}}) {
		t.Errorf("%s holds claims %v after the claim, want %q claimed by %q", ProxyConfig, held.Claims, claimed, surface)
	}
	if !slices.ContainsFunc(stood.commands(), func(command string) bool {
		return strings.Contains(command, quoted("flip"))
	}) {
		t.Errorf("the claim was written and never loaded, so the running proxy answers a hostname nothing on this box says it claims: %v", stood.commands())
	}
}

func TestClaimingAHostnameTwiceWritesTheProxyOnce(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface}}
	stood := claimingBox(t, state)
	if err := stood.host().ClaimHost(context.Background(), HostClaim{Hostname: claimed, Owner: surface}); err != nil {
		t.Fatalf("ClaimHost() = %v", err)
	}
	for _, command := range stood.commands() {
		if writesProxy(command) || strings.Contains(command, quoted("flip")) {
			t.Errorf("a claim already standing rewrote and reloaded the proxy (%q); every reload is a whole-box config post and re-posting one that changes nothing is a window for nothing", command)
		}
	}
}

func TestAClaimTheProxyRefusesLeavesTheFileTheProxyWouldRestartOnto(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, routed())
	previous := stood.held
	stood.broke = func(command string) error {
		if strings.Contains(command, quoted("flip")) {
			return errors.New("the proxy would not take it")
		}
		return nil
	}

	if err := stood.host().ClaimHost(context.Background(), HostClaim{Hostname: claimed, Owner: surface}); err == nil {
		t.Fatal("ClaimHost() succeeded against a proxy that refused the config")
	}
	if stood.held != previous {
		t.Errorf("%s was left carrying a config the running proxy refused, and this host restarts its proxy onto this file rather than onto what it last loaded:\n%s", ProxyConfig, stood.held)
	}
}

func TestDisclaimingAHostnameTakesTheClaimAndLeavesTheRest(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface}, {Hostname: "other.example.com", Owner: surface}}
	stood := claimingBox(t, state)
	if err := stood.host().DisclaimHost(context.Background(), claimed, surface); err != nil {
		t.Fatalf("DisclaimHost() = %v", err)
	}

	held, err := ReadProxyState([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(held.Claims, []HostClaim{{Hostname: "other.example.com", Owner: surface}}) {
		t.Errorf("the claims left are %v, want the one the disclaim never named", held.Claims)
	}
}

func TestAHostnameAnotherSurfaceHoldsIsRefusedRatherThanTakenOffIt(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface}}
	stood := claimingBox(t, state)
	standing := stood.held

	err := stood.host().ClaimHost(context.Background(), HostClaim{Hostname: claimed, Owner: otherSurface})
	if err == nil {
		t.Fatal("a second project bound a hostname the first one already holds, and the first project's site then answers nothing with no deploy of its own having failed")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeBusy {
		t.Errorf("the second claim failed with %v, want %s naming who holds it", err, providerkit.CodeBusy)
	}
	if !strings.Contains(err.Error(), surface) {
		t.Errorf("the second claim is refused with\n%s\nand never names the surface that already holds %s, which is where the user has to unbind it", err, claimed)
	}
	if stood.held != standing {
		t.Errorf("%s was rewritten by a claim that was refused:\n%s", ProxyConfig, stood.held)
	}
}

func TestUnbindingAHostnameAnotherSurfaceNowHoldsLeavesItWhereItIs(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: otherSurface}}
	stood := claimingBox(t, state)

	if err := stood.host().DisclaimHost(context.Background(), claimed, surface); err != nil {
		t.Fatalf("DisclaimHost() = %v", err)
	}
	held, err := ReadProxyState([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(held.Claims, state.Claims) {
		t.Errorf("the claims left are %v, want %v: %s was rebound to another project since, and an unbind or a destroy here takes that project's hostname off the box", held.Claims, state.Claims, claimed)
	}
}

func TestAPreflightThisBoxRefusesIsWhatTheWriteFailsWith(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, routed())
	stood.floor = providerkit.Refuse(providerkit.CodeNotReady,
		"ada cannot run sudo without a password on ocelbox, and every write ocel makes here needs it")

	err := stood.host().ClaimHost(context.Background(), HostClaim{Hostname: claimed, Owner: surface})
	if err == nil {
		t.Fatal("a claim was written on a box whose preflight refused")
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("the claim failed with\n%s\nwant the preflight's own refusal: swallowing it downgrades the write to an unelevated one and the user is handed a shell permission error against a root-owned file instead", err)
	}
	if stood.at(`cat > "$staged"`) >= 0 {
		t.Errorf("a box whose preflight refused was still written to unelevated: %v", stood.commands())
	}
}

func TestTheClaimsThisBoxHoldsAreReadFromWhatTheProxyWasGiven(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface}}
	stood := claimingBox(t, state)

	read, err := stood.host().Claims(context.Background())
	if err != nil {
		t.Fatalf("Claims() = %v", err)
	}
	if !slices.Equal(read, state.Claims) {
		t.Errorf("Claims() = %v, want %v", read, state.Claims)
	}
}

func TestWhatServesAnAppIsTheUpstreamItsRouteNames(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, routed())
	upstream, err := stood.host().Serving(context.Background(), keyed("web"))
	if err != nil {
		t.Fatalf("Serving() = %v", err)
	}
	if upstream != "shop-web-2222:"+AppPort {
		t.Errorf("Serving(web) = %q, want the upstream its route names: a release retires what is serving, and retiring the wrong name drains nothing and stops something live", upstream)
	}
	absent, err := stood.host().Serving(context.Background(), keyed("api"))
	if err != nil {
		t.Fatalf("Serving() = %v", err)
	}
	if absent != "" {
		t.Errorf("Serving(api) = %q, want nothing: an app this box has never served has nothing to retire", absent)
	}
}

func TestAClaimSurvivesTheReleaseThatRewritesTheWholeFile(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface}}
	stood := claimingBox(t, state)

	rel := Release{
		RouteKey:      keyed("web"),
		Target:        "shop-web-3333:" + AppPort,
		Retire:        "shop-web-2222:" + AppPort,
		HealthPath:    "/healthz",
		DeployTimeout: DeployWindow,
		DrainTimeout:  DrainWindow,
	}
	if err := stood.host().Release(context.Background(), rel, nil); err != nil {
		t.Fatalf("Release() = %v", err)
	}

	held, err := ReadProxyState([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(held.Claims, state.Claims) {
		t.Errorf("the claims left after a release are %v, want the %v that stood before it: a deploy renders this file whole and a hostname dropped there is a hostname nothing on this box claims", held.Claims, state.Claims)
	}
}

const otherSurface = "ocel--blog--production"

func twoProjects() ProxyState {
	return ProxyState{
		Grace: DrainWindow,
		Routes: []AppRoute{
			{RouteKey: keyed("web"), Upstream: "shop-web-2222:" + AppPort},
			{RouteKey: RouteKey{Owner: otherSurface, Pointer: pointed, App: "web"}, Upstream: "blog-web-3333:" + AppPort},
		},
	}
}

func TestTwoProjectsRunningTheSameAppNameOnOneBoxKeepTheirOwnRoutes(t *testing.T) {
	t.Parallel()

	read, err := ReadProxyState(mustRender(t, twoProjects()))
	if err != nil {
		t.Fatalf("ReadProxyState() = %v", err)
	}
	if want := slices.SortedFunc(slices.Values(twoProjects().Routes), byKey); !slices.Equal(read.Routes, want) {
		t.Fatalf("the routes read back as %v, want %v: two projects on one box name their apps whatever they like, and a route keyed on the app name alone is one project's route answering for both", read.Routes, want)
	}
	var identities []string
	for _, route := range read.Routes {
		identities = append(identities, route.identity())
	}
	if identities[0] == identities[1] {
		t.Errorf("both projects' routes are written as %q, so one deploy rewrites the other's upstream", identities[0])
	}
}

func TestADeployOfOneProjectLeavesAnotherProjectsRouteWhereItFoundIt(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, twoProjects())
	blog := RouteKey{Owner: otherSurface, Pointer: pointed, App: "web"}
	if err := stood.host().Release(context.Background(), Release{
		RouteKey:      blog,
		Target:        "blog-web-4444:" + AppPort,
		Retire:        "blog-web-3333:" + AppPort,
		HealthPath:    "/healthz",
		DeployTimeout: DeployWindow,
		DrainTimeout:  DrainWindow,
	}, nil); err != nil {
		t.Fatalf("Release() = %v", err)
	}

	held, err := ReadProxyState([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(held.Routes, func(route AppRoute) bool { return route.RouteKey == keyed("web") })
	if at < 0 || held.Routes[at].Upstream != "shop-web-2222:"+AppPort {
		t.Errorf("after a deploy of %s the routes read %v; the other project's app is named web too, and its live container is what a deploy that took its route would then stop", otherSurface, held.Routes)
	}
	if stood.at("docker stop "+quoted("shop-web-2222")) >= 0 {
		t.Errorf("the deploy stopped another project's live container: %v", stood.commands())
	}
}

func TestAppRoutesAreReachedByTheHostnamesTheirOwnSurfaceClaims(t *testing.T) {
	t.Parallel()

	state := twoProjects()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface}, {Hostname: "blog.example.com", Owner: otherSurface}}
	rendered := mustRender(t, state)

	var read struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct{ Routes []caddyRoute } `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(rendered, &read); err != nil {
		t.Fatal(err)
	}
	for _, route := range read.Apps.HTTP.Servers[proxyServer].Routes {
		if len(route.Handle) == 0 || len(route.Handle[0].Upstreams) == 0 {
			continue
		}
		if len(route.Match) != 1 {
			t.Fatalf("the route %q forwards and matches %v; with two projects on this box an unmatched forwarding route answers for both of them", route.Identity, route.Match)
		}
		want := claimed
		if strings.Contains(route.Identity, otherSurface) {
			want = "blog.example.com"
		}
		if !slices.Equal(route.Match[0].Host, []string{want}) {
			t.Errorf("the route %q answers %v, want the %q its own surface claims", route.Identity, route.Match[0].Host, want)
		}
	}
}

func TestEveryBoxRefusesTheHostnamesNothingOnItClaimsAndNamesItselfDoingIt(t *testing.T) {
	t.Parallel()

	for _, box := range []struct {
		what  string
		state ProxyState
	}{
		{"a box serving nothing", ProxyState{Grace: DrainWindow}},
		{"a box serving one project", routed()},
		{"a box serving two projects", twoProjects()},
	} {
		t.Run(box.what, func(t *testing.T) {
			t.Parallel()

			var read struct {
				Apps struct {
					HTTP struct {
						Servers map[string]struct{ Routes []caddyRoute } `json:"servers"`
					} `json:"http"`
				} `json:"apps"`
			}
			if err := json.Unmarshal(mustRender(t, box.state), &read); err != nil {
				t.Fatal(err)
			}
			routes := read.Apps.HTTP.Servers[proxyServer].Routes
			last := routes[len(routes)-1]
			if last.Identity != boxIdentity || len(last.Match) != 0 {
				t.Fatalf("%s ends its routes at %q matching %v, want the box's own named default last: every configuration renders it, so what the bare address answers is one decision and never a count of the routes that happen to stand", box.what, last.Identity, last.Match)
			}
			unmatched := 0
			for _, route := range routes {
				if len(route.Match) == 0 {
					unmatched++
				}
			}
			if unmatched != 1 {
				t.Errorf("%s renders %d routes matching no hostname, want the box's default alone: a second one is dead configuration and a forwarding one hands whichever app sorted first every hostname pointed at this machine", box.what, unmatched)
			}
			if handled := last.Handle; len(handled) != 1 || handled[0].Handler != "static_response" || len(handled[0].Upstreams) != 0 {
				t.Fatalf("%s answers its bare address with %v, want a static refusal: a box forwards a hostname a project claimed and nothing else, which is what Facts().ServesUnbound = false says of it", box.what, handled)
			}
			if last.Handle[0].Status != http.StatusNotFound {
				t.Errorf("%s answers its bare address with %d, want 404: an empty 200 reads as healthy to every uptime check pointed at it", box.what, last.Handle[0].Status)
			}
			if named := last.Handle[0].Headers[EdgeHeader]; !slices.Equal(named, []string{EdgeName}) {
				t.Errorf("%s answers its bare address carrying %s: %v, want %q: the refusal names the edge that made it and says nothing about what else this box serves", box.what, EdgeHeader, named, EdgeName)
			}
		})
	}
}

func TestARouteOnlyOneSurfaceOwnsIsTakenByThatSurfaceAlone(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, twoProjects())
	if err := stood.host().UnroutePointer(context.Background(), surface, pointed); err != nil {
		t.Fatalf("UnroutePointer() = %v", err)
	}
	held, err := ReadProxyState([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if len(held.Routes) != 1 || held.Routes[0].Owner != otherSurface {
		t.Errorf("the routes left are %v, want the other project's alone", held.Routes)
	}
}

func TestATornDownSurfaceLeavesNoRouteForwardingToARemovedContainer(t *testing.T) {
	t.Parallel()

	state := twoProjects()
	state.Routes = append(state.Routes, AppRoute{
		RouteKey: RouteKey{Owner: surface, Pointer: pointed, App: "worker"},
		Upstream: "shop-worker-5555:" + AppPort,
	})
	stood := claimingBox(t, state)
	if err := stood.host().UnrouteSurface(context.Background(), surface); err != nil {
		t.Fatalf("UnrouteSurface() = %v", err)
	}
	held, err := ReadProxyState([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if len(held.Routes) != 1 || held.Routes[0].Owner != otherSurface {
		t.Errorf("the routes left after a teardown of %s are %v; every container it ran is gone, and a route left behind forwards to nothing forever", surface, held.Routes)
	}
}

func TestAConfigComposedOntoAFileAnotherDeployHasSinceRewrittenIsRefusedRatherThanPosted(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, routed())
	moved := mustRender(t, twoProjects())
	stood.after = func(b *bench, command string) {
		if !readsProxy(command) {
			return
		}
		stood.mu.Lock()
		stood.held = string(moved)
		stood.mu.Unlock()
	}

	err := stood.host().ClaimHost(context.Background(), HostClaim{Hostname: claimed, Owner: surface})
	if err == nil {
		t.Fatal("a claim composed onto a configuration another deploy had already replaced was written anyway")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeBusy {
		t.Errorf("the write was refused with %v, want %s: this file is the whole box's, every writer renders it whole, and the loser must be told rather than drop the winner's routes", err, providerkit.CodeBusy)
	}
	if stood.held != string(moved) {
		t.Errorf("%s was left as\n%s\nwant what the deploy that moved it wrote: the write stages beside the file and checks the digest before it moves anything into place", ProxyConfig, stood.held)
	}
	if slices.ContainsFunc(stood.commands(), func(command string) bool { return strings.Contains(command, quoted("flip")) }) {
		t.Errorf("a write that was refused still posted a configuration to the running proxy: %v", stood.commands())
	}
}
