package host

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const (
	claimed = "shop.example.com"
	surface = "ocel--shop--production"
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
	stood.answer = func(command string) (session.Result, bool) {
		switch {
		case strings.HasPrefix(command, "cat "+quoted(ProxyConfig)):
			return session.Result{Stdout: stood.held}, true
		case strings.Contains(command, "cat > "+quoted(ProxyConfig)):
			stood.mu.Lock()
			stood.held = stood.fed[len(stood.fed)-1]
			stood.mu.Unlock()
			return session.Result{}, true
		default:
			return session.Result{}, false
		}
	}
	return stood
}

func routed() ProxyState {
	return ProxyState{
		Grace:  DrainWindow,
		Routes: []AppRoute{{App: "web", Upstream: "shop-web-2222:" + AppPort}},
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
	if len(routes) != 2 {
		t.Fatalf("the rendered server carries %d routes, want the claim and the app route: %s", len(routes), rendered)
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
		`"@id":"`+routeIdentity+`web"`,
		`"@id":"`+claimIdentity+surface+claimSeparator+claimed+`"`, 1)
	if _, err := ReadProxyState([]byte(document)); err == nil {
		t.Error("a claim carrying an app's forwarding handler reads back as a claim, and a deploy that rewrites this file whole would drop what it forwards to")
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
		if strings.Contains(command, "cat > "+quoted(ProxyConfig)) || strings.Contains(command, quoted("flip")) {
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
	if err := stood.host().DisclaimHost(context.Background(), claimed); err != nil {
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
	upstream, err := stood.host().Serving(context.Background(), "web")
	if err != nil {
		t.Fatalf("Serving() = %v", err)
	}
	if upstream != "shop-web-2222:"+AppPort {
		t.Errorf("Serving(web) = %q, want the upstream its route names: a release retires what is serving, and retiring the wrong name drains nothing and stops something live", upstream)
	}
	absent, err := stood.host().Serving(context.Background(), "api")
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
	stood := benchedOver(t, mustRender(t, state))

	rel := Release{
		App:           "web",
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

func benchedOver(t *testing.T, document []byte) *claimBench {
	t.Helper()
	stood := claimingBox(t, ProxyState{Grace: DrainWindow})
	stood.mu.Lock()
	stood.held = string(document)
	stood.mu.Unlock()
	return stood
}
