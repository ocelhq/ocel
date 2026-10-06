package alb

import (
	"context"
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
		Service: "ocel-shop-prod-web", Tag: "r0001abcd",
	}
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

	if refusalCodeOf(err) != refusal.CodeNotReady || !strings.Contains(err.Error(), "ocel deployments prune") {
		t.Errorf("RouteOriginHost = %v, want a not-ready refusal naming `ocel deployments prune`", err)
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
