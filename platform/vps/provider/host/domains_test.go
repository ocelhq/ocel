package host

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
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

func claimingBox(t *testing.T, state RoutingTable) *claimBench {
	t.Helper()

	mustRender(t, state)
	stood := &claimBench{bench: machine(nil), held: string(mustWrite(t, state))}
	stood.answer = servesProxy(stood.bench, &stood.held)
	return stood
}

func routed() RoutingTable {
	return RoutingTable{
		Grace:  DrainWindow,
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: "shop-web-2222:" + providerkit.InjectedPortText}},
	}
}

func TestAClaimedHostnameReadsBackAsTheSurfaceThatClaimedIt(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	read, err := ReadRoutingTable(mustWrite(t, state))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
	}
	if !slices.Equal(read.Claims, state.Claims) {
		t.Errorf("the claims read back as %v, want the %v that were written: this file is the only thing that answers which surface claims a hostname", read.Claims, state.Claims)
	}
	if !slices.Equal(read.Routes, state.Routes) {
		t.Errorf("the routes read back as %v, want %v: a claim must not cost the app it sits beside its route", read.Routes, state.Routes)
	}
}

func TestAClaimedHostnameIsHeldByTheFrontProxyAndAnsweredByTheAppItsSurfaceRuns(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	if upstream, ok := routedBy(t, state)(claimed, "/"); !ok || upstream != state.Routes[0].Upstream {
		t.Errorf("the switchboard answers %s from %q (%v), want the app its surface runs on %s", claimed, upstream, ok, state.Routes[0].Upstream)
	}
	if hosts := admission(state).Entries; len(hosts) != 1 || hosts[0].Hostname != claimed {
		t.Errorf("the front proxy is admitted %v, want %s alone: a hostname it holds no certificate for is one https never reaches", hosts, claimed)
	}
	unrouted := state
	unrouted.Routes = nil
	if hosts := admission(unrouted).Entries; len(hosts) != 1 || hosts[0].Hostname != claimed {
		t.Errorf("a hostname claimed before anything serves it is admitted as %v, want it held all the same: the certificate is ordered at the bind, not at the first deploy", hosts)
	}
}

func TestASurfaceNamedWithTheSeparatorIsRefusedRatherThanRenderedAmbiguously(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: "ocel" + switchboard.ClaimSeparator + "shop", Pointer: pointed}}
	if _, err := RenderProxyConfig(caddy.Builtin{}, state); err == nil {
		t.Errorf("a surface named with %q renders a claim that reads back naming a different surface", switchboard.ClaimSeparator)
	}
}

func TestAHostnameNamedWithTheSeparatorIsRefusedTheWayASurfaceIs(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: "shop.example.com" + switchboard.ClaimSeparator + surface, Owner: surface, Pointer: pointed}}
	if _, err := RenderProxyConfig(caddy.Builtin{}, state); err == nil {
		t.Errorf("a hostname carrying %q renders a claim whose identity reads back as a different surface and host; the surface half of the same identity is already refused for it", switchboard.ClaimSeparator)
	}
}

func TestClaimingAHostnameLoadsItOntoTheRunningProxy(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, routed())
	if err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}

	held, err := ReadRoutingTable([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(held.Claims, []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}) {
		t.Errorf("%s holds claims %v after the claim, want %q claimed by %q", ProxyConfig, held.Claims, claimed, surface)
	}
	if !slices.ContainsFunc(stood.commands(), loadsSwitchboard) || !slices.ContainsFunc(stood.commands(), reloadsFront) {
		t.Errorf("the claim was written and never loaded, so the running proxy answers a hostname nothing on this box says it claims: %v", stood.commands())
	}
}

func TestClaimingAHostnameTwiceWritesTheProxyOnce(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	stood := claimingBox(t, state)
	if err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	for _, command := range stood.commands() {
		if writesProxy(command) || loadsSwitchboard(command) || reloadsFront(command) {
			t.Errorf("a claim already standing rewrote and reloaded the proxy (%q); every reload is a whole-box config post and re-posting one that changes nothing is a window for nothing", command)
		}
	}
}

func TestAClaimTheProxyRefusesLeavesTheFileTheProxyWouldRestartOnto(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, routed())
	previous := stood.held
	stood.broke = func(command string) error {
		if reloadsFront(command) {
			return errors.New("the proxy would not take it")
		}
		return nil
	}

	if err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err == nil {
		t.Fatal("ClaimHosts() succeeded against a proxy that refused the config")
	}
	if stood.held != previous {
		t.Errorf("%s was left carrying a config the running proxy refused, and this host restarts its proxy onto this file rather than onto what it last loaded:\n%s", ProxyConfig, stood.held)
	}
}

func TestDisclaimingAHostnameTakesTheClaimAndLeavesTheRest(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}, {Hostname: "other.example.com", Owner: surface, Pointer: pointed}}
	stood := claimingBox(t, state)
	if err := stood.host().DisclaimHost(context.Background(), claimed, surface); err != nil {
		t.Fatalf("DisclaimHost() = %v", err)
	}

	held, err := ReadRoutingTable([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(held.Claims, []HostClaim{{Hostname: "other.example.com", Owner: surface, Pointer: pointed}}) {
		t.Errorf("the claims left are %v, want the one the disclaim never named", held.Claims)
	}
}

func TestDisclaimingASurfaceTakesEveryHostnameItHoldsAndNoOneElses(t *testing.T) {
	t.Parallel()

	state := routed()
	kept := HostClaim{Hostname: "kept.example.com", Owner: otherSurface, Pointer: pointed}
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}, kept, {Hostname: "other.example.com", Owner: surface, Pointer: pointed}}
	stood := claimingBox(t, state)
	if err := stood.host().DisclaimSurface(context.Background(), surface); err != nil {
		t.Fatalf("DisclaimSurface() = %v", err)
	}

	held, err := ReadRoutingTable([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(held.Claims, []HostClaim{kept}) {
		t.Errorf("the claims left are %v, want only %v: a torn-down surface answers for nothing, and every hostname it held has to come back into circulation without a shell on this box", held.Claims, kept)
	}
}

func TestAHostnameAnotherSurfaceHoldsIsRefusedRatherThanTakenOffIt(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	stood := claimingBox(t, state)
	standing := stood.held

	err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: otherSurface, Pointer: pointed}})
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
	state.Claims = []HostClaim{{Hostname: claimed, Owner: otherSurface, Pointer: pointed}}
	stood := claimingBox(t, state)

	if err := stood.host().DisclaimHost(context.Background(), claimed, surface); err != nil {
		t.Fatalf("DisclaimHost() = %v", err)
	}
	held, err := ReadRoutingTable([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(held.Claims, state.Claims) {
		t.Errorf("the claims left are %v, want %v: %s was rebound to another project since, and an unbind or a destroy here takes that project's hostname off the box", held.Claims, state.Claims, claimed)
	}
}

func TestAHostnameItsOwnerReleasesIsFreeForTheNextSurfaceToTake(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	stood := claimingBox(t, state)
	ctx := context.Background()

	if err := stood.host().DisclaimHost(ctx, claimed, surface); err != nil {
		t.Fatalf("DisclaimHost() = %v", err)
	}
	if err := stood.host().ClaimHosts(ctx, []HostClaim{{Hostname: claimed, Owner: otherSurface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() by the surface it was released for = %v: a hostname one project unbinds is a hostname another can bind, and a box that keeps refusing it has taken the name out of circulation for good", err)
	}

	held, err := ReadRoutingTable([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(held.Claims, []HostClaim{{Hostname: claimed, Owner: otherSurface, Pointer: pointed}}) {
		t.Errorf("the claims left are %v, want %s held by %s alone", held.Claims, claimed, otherSurface)
	}
}

func refusingSudo(t *testing.T) *claimBench {
	t.Helper()

	stood := claimingBox(t, routed())
	stood.floor = providerkit.Refuse(providerkit.CodeNotReady,
		"ada cannot run sudo without a password on ocelbox, and every write ocel makes here needs it")
	return stood
}

func TestALoginThatCannotElevateStillWritesAProxyConfigItOwns(t *testing.T) {
	t.Parallel()

	stood := refusingSudo(t)
	if err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts = %v; a deploy login that owns %s writes it without sudo, and that is how a box provisioned for a non-root login works rather than a state to refuse", err, ProxyConfig)
	}

	held, err := ReadRoutingTable([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(held.Claims, HostClaim{Hostname: claimed, Owner: surface, Pointer: pointed}) {
		t.Errorf("the claims on the box are %v, want %s among them", held.Claims, claimed)
	}
	if at := stood.at(`IFS= read -r written`); at < 0 || strings.HasPrefix(stood.commands()[at], "sudo") {
		t.Errorf("the write went out as %v, want one unelevated command", stood.commands())
	}
}

func TestAWriteThisLoginCannotMakeNamesTheElevationItWasRefused(t *testing.T) {
	t.Parallel()

	stood := refusingSudo(t)
	answering := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		if writesProxy(command) {
			return session.Result{Code: 1, Stderr: "cannot create " + ProxyConfig + ".XXXXXX: Permission denied"}, true
		}
		return answering(command)
	}

	err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}})
	if err == nil {
		t.Fatal("a claim this login could neither write nor elevate to write reported success")
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("the claim failed with\n%s\nand never names the elevation that was refused: a bare shell permission error against a root-owned file leaves the user nothing to fix", err)
	}
	if !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("the claim failed with\n%s\nand never says what the box refused", err)
	}
}

func TestTheClaimsThisBoxHoldsAreReadFromWhatTheProxyWasGiven(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
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
	if upstream != "shop-web-2222:"+providerkit.InjectedPortText {
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
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	stood := claimingBox(t, state)

	rel := Release{
		Apps:          []AppRelease{{RouteKey: keyed("web"), Target: "shop-web-3333:" + providerkit.InjectedPortText, HealthPath: "/healthz"}},
		DeployTimeout: DeployWindow,
		DrainTimeout:  DrainWindow,
	}
	if err := stood.host().Release(context.Background(), rel, nil); err != nil {
		t.Fatalf("Release() = %v", err)
	}

	held, err := ReadRoutingTable([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(held.Claims, state.Claims) {
		t.Errorf("the claims left after a release are %v, want the %v that stood before it: a deploy renders this file whole and a hostname dropped there is a hostname nothing on this box claims", held.Claims, state.Claims)
	}
}

const otherSurface = "ocel--blog--production"

func twoProjects() RoutingTable {
	return RoutingTable{
		Grace: DrainWindow,
		Routes: []AppRoute{
			{RouteKey: keyed("web"), Upstream: "shop-web-2222:" + providerkit.InjectedPortText},
			{RouteKey: RouteKey{Owner: otherSurface, Pointer: pointed, App: "web"}, Upstream: "blog-web-3333:" + providerkit.InjectedPortText},
		},
	}
}

func TestTwoProjectsRunningTheSameAppNameOnOneBoxKeepTheirOwnRoutes(t *testing.T) {
	t.Parallel()

	read, err := ReadRoutingTable(mustWrite(t, twoProjects()))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
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
		Apps:          []AppRelease{{RouteKey: blog, Target: "blog-web-4444:" + providerkit.InjectedPortText, HealthPath: "/healthz"}},
		DeployTimeout: DeployWindow,
		DrainTimeout:  DrainWindow,
	}, nil); err != nil {
		t.Fatalf("Release() = %v", err)
	}

	held, err := ReadRoutingTable([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(held.Routes, func(route AppRoute) bool { return route.RouteKey == keyed("web") })
	if at < 0 || held.Routes[at].Upstream != "shop-web-2222:"+providerkit.InjectedPortText {
		t.Errorf("after a deploy of %s the routes read %v; the other project's app is named web too, and its live container is what a deploy that took its route would then stop", otherSurface, held.Routes)
	}
	if stood.at("docker stop "+quoted("shop-web-2222")) >= 0 {
		t.Errorf("the deploy stopped another project's live container: %v", stood.commands())
	}
}

func TestAppRoutesAreReachedByTheHostnamesTheirOwnSurfaceClaims(t *testing.T) {
	t.Parallel()

	state := twoProjects()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}, {Hostname: "blog.example.com", Owner: otherSurface, Pointer: pointed}}
	answer := routedBy(t, state)
	for hostname, owner := range map[string]string{claimed: surface, "blog.example.com": otherSurface} {
		at := slices.IndexFunc(state.Routes, func(route AppRoute) bool { return route.Owner == owner })
		if upstream, ok := answer(hostname, "/"); !ok || upstream != state.Routes[at].Upstream {
			t.Errorf("%s is answered from %q (%v), want %s's own app on %s: with two projects on this box, a route answering a hostname its surface never claimed answers for both of them",
				hostname, upstream, ok, owner, state.Routes[at].Upstream)
		}
	}
}

func twoApps() RoutingTable {
	return RoutingTable{
		Grace: DrainWindow,
		Routes: []AppRoute{
			{RouteKey: keyed("api"), Upstream: "shop-api-1111:" + providerkit.InjectedPortText},
			{RouteKey: keyed("web"), Upstream: "shop-web-2222:" + providerkit.InjectedPortText},
		},
	}
}

func TestOneSurfacesHostnameIsNotHandedToEveryAppThatSurfaceRuns(t *testing.T) {
	t.Parallel()

	state := twoApps()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}

	_, err := RenderProxyConfig(caddy.Builtin{}, state)
	if err == nil {
		t.Fatal("a project running two apps rendered both of them matching every hostname it claims; reverse_proxy is terminal and the routes are written in name order, so api answers shop.example.com and web is configuration nothing on this box ever reaches")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Errorf("the render failed with %v, want %s", err, providerkit.CodeInvalid)
	}
	for _, named := range []string{"api", "web", claimed} {
		if !strings.Contains(err.Error(), named) {
			t.Errorf("the render is refused with\n%s\nand never names %s", err, named)
		}
	}
}

func TestAHostnameDeclaredUnderOneAppOfAMultiAppSurfaceReachesThatAppAlone(t *testing.T) {
	t.Parallel()

	state := twoApps()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed, App: "api"}}

	if upstream, ok := routedBy(t, state)(claimed, "/"); !ok || upstream != state.Routes[0].Upstream {
		t.Errorf("%s is answered from %q (%v), want api on %s alone: the project declared the hostname under api, and a box that cannot honour the declaration refuses every multi-app project a domain at all", claimed, upstream, ok, state.Routes[0].Upstream)
	}
}

func TestAClaimNamingTheAppItWasDeclaredUnderReadsBackCarryingThatApp(t *testing.T) {
	t.Parallel()

	state := twoApps()
	state.Claims = []HostClaim{
		{Hostname: claimed, Owner: surface, Pointer: pointed, App: "api"},
		{Hostname: "www.example.com", Owner: surface, Pointer: pointed, App: "web"},
	}

	read, err := ReadRoutingTable(mustWrite(t, state))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
	}
	want := slices.SortedFunc(slices.Values(state.Claims), func(a, b HostClaim) int {
		return strings.Compare(a.Hostname, b.Hostname)
	})
	if !slices.Equal(read.Claims, want) {
		t.Errorf("the claims read back as %v, want %v: the rendered configuration is the box's only record of which app a hostname was declared under, so an app it does not carry is one the next deploy cannot restore", read.Claims, want)
	}
}

func TestAProjectWideClaimStillNamesNoAppOnTheWireItIsWrittenTo(t *testing.T) {
	t.Parallel()

	wide := routed()
	wide.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	if written := string(mustWrite(t, wide)); strings.Contains(written, `"app":""`) || strings.Count(written, `"app"`) != len(wide.Routes) {
		t.Errorf("a project-wide claim is written as\n%s\nwant it carrying no app at all", written)
	}
	attributed := routed()
	attributed.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed, App: "web"}}
	if written := string(mustWrite(t, attributed)); strings.Count(written, `"app":"web"`) != 2 {
		t.Errorf("a claim declared under an app is written as\n%s\nwithout the app, so nothing distinguishes it from the project-wide claim it is not", written)
	}
}

func TestAnUnattributedHostnameOnAMultiAppSurfaceIsRefusedAndNamesTheFormThatFixesIt(t *testing.T) {
	t.Parallel()

	state := twoApps()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}

	_, err := RenderProxyConfig(caddy.Builtin{}, state)
	if err == nil {
		t.Fatal("a project running two apps and claiming a hostname project-wide rendered both routes matching it")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("the render failed with %v, want %s", err, providerkit.CodeInvalid)
	}
	if !strings.Contains(err.Error(), "domains.production") {
		t.Errorf("the refusal is\n%s\nand never names domains.production: the config form that fixes this is declaring the hostname under the app, and a refusal that does not say so reads as a box that cannot serve multi-app projects", err)
	}
}

func TestTwoAppsOnASurfaceThatClaimsNothingAreBothStillWritten(t *testing.T) {
	t.Parallel()

	read, err := ReadRoutingTable(mustWrite(t, twoApps()))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
	}
	if len(read.Routes) != 2 {
		t.Errorf("a project running two apps and claiming no hostname renders %v; neither route answers a hostname yet, and refusing the pair here would stop every multi-app deploy on a box that has no domain bound at all", read.Routes)
	}
}

func TestEveryBoxRefusesTheHostnamesNothingOnItClaimsAndForwardsThemToTheSwitchboardToSaySo(t *testing.T) {
	t.Parallel()

	for _, box := range []struct {
		what  string
		state RoutingTable
	}{
		{"a box serving nothing", RoutingTable{Grace: DrainWindow}},
		{"a box serving one project", routed()},
		{"a box serving two projects", twoProjects()},
	} {
		t.Run(box.what, func(t *testing.T) {
			t.Parallel()

			if upstream, ok := routedBy(t, box.state)("unclaimed.example.com", "/"); ok {
				t.Errorf("%s answers a hostname nothing claims from %q, want it refused", box.what, upstream)
			}
			var read struct {
				Apps struct {
					HTTP struct {
						Servers map[string]struct {
							Routes []struct {
								Match  []json.RawMessage `json:"match"`
								Handle []struct {
									Upstreams []struct {
										Dial string `json:"dial"`
									} `json:"upstreams"`
								} `json:"handle"`
							} `json:"routes"`
						} `json:"servers"`
					} `json:"http"`
				} `json:"apps"`
			}
			if err := json.Unmarshal(mustRender(t, box.state), &read); err != nil {
				t.Fatal(err)
			}
			for _, server := range read.Apps.HTTP.Servers {
				last := server.Routes[len(server.Routes)-1]
				if len(last.Match) != 0 || len(last.Handle) != 1 || len(last.Handle[0].Upstreams) != 1 || last.Handle[0].Upstreams[0].Dial != SwitchboardUpstream {
					t.Errorf("%s ends its front routes with %+v, want an unmatched forward to %s: the switchboard answers what nothing claims with the box's own 404, and caddy's own answer is an empty 200", box.what, last, SwitchboardUpstream)
				}
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
	held, err := ReadRoutingTable([]byte(stood.held))
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
		Upstream: "shop-worker-5555:" + providerkit.InjectedPortText,
	})
	stood := claimingBox(t, state)
	if err := stood.host().UnrouteSurface(context.Background(), surface); err != nil {
		t.Fatalf("UnrouteSurface() = %v", err)
	}
	held, err := ReadRoutingTable([]byte(stood.held))
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
	moved := mustWrite(t, twoProjects())
	proxied := servesProxy(stood.bench, &stood.held)
	stood.answer = func(command string) (session.Result, bool) {
		if !writesProxy(command) {
			return proxied(command)
		}
		stood.mu.Lock()
		stood.held = string(moved)
		stood.mu.Unlock()
		return session.Result{Code: routingMoved, Stderr: digested(string(moved))}, true
	}

	err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}})
	if err == nil {
		t.Fatal("a claim composed onto a configuration another deploy kept replacing was written anyway")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeBusy {
		t.Errorf("the write was refused with %v, want %s: this file is the whole box's, every writer renders it whole, and the loser must be told rather than drop the winner's routes", err, providerkit.CodeBusy)
	}
	if stood.held != string(moved) {
		t.Errorf("%s was left as\n%s\nwant what the deploy that moved it wrote: the write stages beside the file and checks the digest before it moves anything into place", ProxyConfig, stood.held)
	}
	if slices.ContainsFunc(stood.commands(), func(command string) bool { return loadsSwitchboard(command) || reloadsFront(command) }) {
		t.Errorf("a write that was refused still posted a configuration to the running proxy: %v", stood.commands())
	}
}

func TestAWildcardIsRefusedAsAnOrdinaryClaim(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, routed())
	err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: "*.preview.acme.com", Owner: surface, Pointer: pointed}})
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("ClaimHosts() of a wildcard = %v, want a refusal: a claim is a hostname the proxy orders one certificate for over http-01, and a wildcard is a match every hostname pointed at this box falls under", err)
	}
	if stood.count(loadsSwitchboard) > 0 || strings.Contains(stood.held, "*.preview") {
		t.Errorf("a refused wildcard claim still reached the proxy: %v", stood.commands())
	}
}

func flippingBox(t *testing.T, config *string, flipped func(at int) session.Result) *claimBench {
	t.Helper()

	stood := claimingBox(t, routed())
	proxied := servesPair(stood.bench, &stood.held, config)
	flips := 0
	stood.answer = func(command string) (session.Result, bool) {
		if reloadsFront(command) {
			flips++
			return flipped(flips), true
		}
		return proxied(command)
	}
	return stood
}

func TestAReloadThatFailsPutsBackTheExactBytesOfBothFilesAndReloadsThem(t *testing.T) {
	t.Parallel()

	older := olderRendering(t, routed())
	config := older
	stood := flippingBox(t, &config, func(at int) session.Result {
		if at == 1 {
			return session.Result{Code: 1, Stderr: "the connection dropped before the reload answered"}
		}
		return session.Result{}
	})
	table := stood.held

	if err := stood.host().rerender(context.Background()); err == nil {
		t.Fatal("rerender() over a reload that failed = nil, want the failure")
	}
	if stood.held != table || config != older {
		t.Errorf("a reload that failed left\n%s\n%s\nwant both files byte for byte as they stood: the rendering it failed to reload stays on disk as if current, the next write skips it, and the proxy loads it on its next restart",
			stood.held, config)
	}
	commands := stood.commands()
	restored := -1
	for at, command := range commands {
		if writesProxy(command) {
			restored = at
		}
	}
	if restored < 0 || !slices.ContainsFunc(commands[restored:], reloadsFront) || !slices.ContainsFunc(commands[restored:], loadsSwitchboard) {
		t.Errorf("the files were put back and the proxy never reloaded them: a reload whose answer was lost may have loaded the rendering all the same, and the proxy then serves routes no table records: %v", commands)
	}
}

func TestAReloadThatFailsTwiceSaysTheProxyMayServeWhatTheFilesNoLongerRecord(t *testing.T) {
	t.Parallel()

	config := olderRendering(t, routed())
	stood := flippingBox(t, &config, func(int) session.Result {
		return session.Result{Code: 1, Stderr: "the connection dropped before the reload answered"}
	})
	err := stood.host().rerender(context.Background())
	if err == nil || !strings.Contains(err.Error(), "may still serve") {
		t.Errorf("rerender() over a reload that failed and a reload back that failed too = %v, want it to say the proxy may still serve what the restored files no longer record", err)
	}
}
