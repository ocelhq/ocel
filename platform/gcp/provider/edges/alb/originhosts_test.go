package alb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const originHostname = "r0001abcd-ocel-shop-prod-web.o.example.com"

func originHost() OriginHost {
	return OriginHost{
		Tier: environment.TierProduction, Slug: "shop", Hostname: originHostname,
		Service: "ocel-shop-prod-web", Tag: "r0001abcd", Revision: "ocel-shop-prod-web-00001-abc",
	}
}

func secondOriginHost() OriginHost {
	host := originHost()
	host.Hostname = "r0002abcd-ocel-shop-prod-web.o.example.com"
	host.Tag = "r0002abcd"
	host.Revision = "ocel-shop-prod-web-00002-def"
	return host
}

func originStack() string {
	return Target{Tier: environment.TierProduction, Slug: "shop", Origin: originHostname}.Name()
}

func TestAnOriginHostnameIsRoutedToTheRevisionTaggedWithItsRelease(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)

	if err := balancer.Shielded().RouteOriginHost(context.Background(), originHost()); err != nil {
		t.Fatalf("RouteOriginHost = %v", err)
	}

	routed := routedTo(w, "ocel-alb-shielded-production-routes", originHostname)
	if routed == "" {
		t.Fatalf("the shielded url map routes nothing for %s", originHostname)
	}
	seen := w.declarations(originStack())
	var neg declaration
	for _, each := range seen {
		if each.Token == "gcp:compute/regionNetworkEndpointGroup:RegionNetworkEndpointGroup" {
			neg = each
		}
	}
	cloudRun, _ := neg.Args["cloudRun"].(map[string]any)
	if cloudRun["service"] != "ocel-shop-prod-web" || cloudRun["tag"] != "r0001abcd" {
		t.Errorf("neg cloudRun = %v, want the service at the release's tag", cloudRun)
	}
	if _, backed := seen[routed]; !backed {
		t.Errorf("the url map routes to %q, which the origin host's stack does not declare", routed)
	}
}

func TestAnOriginHostsBackendKeepsNoCDNCacheOfItsOwn(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)
	if err := balancer.Shielded().RouteOriginHost(context.Background(), originHost()); err != nil {
		t.Fatal(err)
	}

	for name, each := range w.declarations(originStack()) {
		if each.Token != backendToken {
			continue
		}
		if each.Args["enableCdn"] == true || each.Args["cdnPolicy"] != nil {
			t.Errorf("backend %s has Cloud CDN on (%v): the worker caches in front, and an origin cache would serve a stale page after revalidateTag", name, each.Args)
		}
		return
	}
	t.Error("the origin host declares no backend service")
}

func TestRoutingAnOriginHostnameTwiceChangesNothing(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)
	shielded := balancer.Shielded()
	ctx := context.Background()
	if err := shielded.RouteOriginHost(ctx, originHost()); err != nil {
		t.Fatal(err)
	}
	ups := len(w.raised())

	if err := shielded.RouteOriginHost(ctx, originHost()); err != nil {
		t.Fatal(err)
	}
	if got := len(w.raised()); got != ups {
		t.Errorf("a second route raised %d more stacks, want none", got-ups)
	}
}

func TestUnroutingAnOriginHostnameTakesItsRuleBackendAndRecordAway(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)
	shielded := balancer.Shielded()
	ctx := context.Background()
	if err := shielded.RouteOriginHost(ctx, originHost()); err != nil {
		t.Fatal(err)
	}

	if err := shielded.UnrouteOriginHost(ctx, environment.TierProduction, originHostname); err != nil {
		t.Fatalf("UnrouteOriginHost = %v", err)
	}
	if routed := routedTo(w, "ocel-alb-shielded-production-routes", originHostname); routed != "" {
		t.Errorf("the url map still routes the host to %q", routed)
	}
	destroyed := false
	for _, name := range w.torn() {
		destroyed = destroyed || name == originStack()
	}
	if !destroyed {
		t.Errorf("the origin host's stack was not destroyed: %v", w.torn())
	}
	if err := shielded.RouteOriginHost(ctx, originHost()); err != nil {
		t.Errorf("routing it again = %v, want the record forgotten so it routes afresh", err)
	}
}

func TestAnOriginHostnameWhoseRouteFailedIsStillUnroutedAfterwards(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)
	shielded := balancer.Shielded()
	ctx := context.Background()
	w.failRoute(originHostname, errors.New("url map busy"))
	if err := shielded.RouteOriginHost(ctx, originHost()); err == nil {
		t.Fatal("RouteOriginHost succeeded, want the route failure")
	}

	if err := shielded.UnrouteOriginHost(ctx, environment.TierProduction, originHostname); err != nil {
		t.Fatalf("UnrouteOriginHost = %v", err)
	}

	destroyed := false
	for _, name := range w.torn() {
		destroyed = destroyed || name == originStack()
	}
	if !destroyed {
		t.Errorf("the origin host's stack the failed route raised was not destroyed: %v", w.torn())
	}
}

func TestUnroutingARevisionsOriginHostnamesTakesAwayOnlyThatRevisions(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)
	shielded := balancer.Shielded()
	ctx := context.Background()
	first, second := originHost(), secondOriginHost()
	for _, host := range []OriginHost{first, second} {
		if err := shielded.RouteOriginHost(ctx, host); err != nil {
			t.Fatal(err)
		}
	}

	if err := shielded.UnrouteOriginHosts(ctx, environment.TierProduction, first.Service, first.Revision); err != nil {
		t.Fatalf("UnrouteOriginHosts = %v", err)
	}

	if routedTo(w, "ocel-alb-shielded-production-routes", first.Hostname) != "" {
		t.Errorf("the url map still routes %s", first.Hostname)
	}
	if routedTo(w, "ocel-alb-shielded-production-routes", second.Hostname) == "" {
		t.Errorf("the url map lost the rule of %s, which another revision of the service still answers on", second.Hostname)
	}
	if err := shielded.RouteOriginHost(ctx, first); err != nil || len(w.raised()) != 3 {
		t.Errorf("routing the first host again = %v after %d stacks raised, want its record forgotten so it raises afresh", err, len(w.raised()))
	}
}

func TestUnroutingARevisionLeavesAHostnameThatALaterRevisionOfTheSameReleaseNowAnswers(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)
	shielded := balancer.Shielded()
	ctx := context.Background()
	older, newer := originHost(), originHost()
	newer.Revision = "ocel-shop-prod-web-00003-ghi"
	for _, host := range []OriginHost{older, newer} {
		if err := shielded.RouteOriginHost(ctx, host); err != nil {
			t.Fatal(err)
		}
	}

	if err := shielded.UnrouteOriginHosts(ctx, environment.TierProduction, older.Service, older.Revision); err != nil {
		t.Fatalf("UnrouteOriginHosts = %v", err)
	}

	if routedTo(w, "ocel-alb-shielded-production-routes", older.Hostname) == "" {
		t.Errorf("the url map lost %s, which the newer revision of the same release answers on", older.Hostname)
	}
}

func TestUnroutingAServicesOriginHostnamesWithNoRevisionTakesAwayEveryOne(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)
	shielded := balancer.Shielded()
	ctx := context.Background()
	first, second := originHost(), secondOriginHost()
	for _, host := range []OriginHost{first, second} {
		if err := shielded.RouteOriginHost(ctx, host); err != nil {
			t.Fatal(err)
		}
	}

	if err := shielded.UnrouteOriginHosts(ctx, environment.TierProduction, first.Service, ""); err != nil {
		t.Fatalf("UnrouteOriginHosts = %v", err)
	}

	for _, host := range []OriginHost{first, second} {
		if routedTo(w, "ocel-alb-shielded-production-routes", host.Hostname) != "" {
			t.Errorf("the url map still routes %s", host.Hostname)
		}
	}
}

func TestUnroutingAnOriginHostnameNothingRoutedSucceeds(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)

	if err := balancer.Shielded().UnrouteOriginHost(context.Background(), environment.TierProduction, originHostname); err != nil {
		t.Errorf("UnrouteOriginHost = %v, want nothing to do", err)
	}
	if len(w.torn()) != 0 {
		t.Errorf("destroyed %v for a host that was never routed", w.torn())
	}
}

func TestAnOriginHostnameIsRefusedWhenTheURLMapIsFull(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)
	full := map[string]string{}
	for i := range maxHostRules {
		full[fmt.Sprintf("h%d.example.com", i)] = "backend"
	}
	w.routed["ocel-alb-shielded-production-routes"] = full

	err := balancer.Shielded().RouteOriginHost(context.Background(), originHost())

	if refusalCodeOf(err) != refusal.CodeNotReady || !strings.Contains(err.Error(), "ocel promotions prune") {
		t.Errorf("RouteOriginHost = %v, want a not-ready refusal naming `ocel promotions prune`", err)
	}
	if len(w.raised()) != 0 {
		t.Error("a stack was raised for a host the url map has no room for")
	}
}

func TestOnlyTheShieldedLoadBalancerRoutesOriginHostnames(t *testing.T) {
	t.Parallel()
	balancer, _ := shielding(t)

	if err := balancer.RouteOriginHost(context.Background(), originHost()); refusalCodeOf(err) != refusal.CodeInvalid {
		t.Errorf("RouteOriginHost on the unshielded edge = %v, want an invalid refusal", err)
	}
	revisionless := originHost()
	revisionless.Revision = ""
	if err := balancer.Shielded().RouteOriginHost(context.Background(), revisionless); refusalCodeOf(err) != refusal.CodeInvalid {
		t.Errorf("RouteOriginHost with no revision = %v, want an invalid refusal: nothing could find the host again to unroute it", err)
	}
	tagless := originHost()
	tagless.Tag = ""
	if err := balancer.Shielded().RouteOriginHost(context.Background(), tagless); refusalCodeOf(err) != refusal.CodeInvalid {
		t.Errorf("RouteOriginHost with no tag = %v, want an invalid refusal: a tagless host would reach the service's default traffic", err)
	}
}

func routedTo(w *world, urlMap, hostname string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.routed[urlMap][hostname]
}
