package gcp

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const shopBackend = "ocel-alb-shop-production-shop"

func shopRoutes() *alb.StaticRoutes {
	return &alb.StaticRoutes{
		Backend:   "ocel-alb-production-assets",
		Objects:   "/assets/production/shop/web/r1/assets",
		Immutable: []string{"/_next/static/"},
	}
}

func TestRoutingAHostnameWithStaticRoutesSendsItsImmutablePrefixToTheBackendBucketUnderTheReleasesStoragePrefix(t *testing.T) {
	t.Parallel()

	p, server := routing(t, emptyMap())
	if err := p.Route(context.Background(), tierRoutes, "shop.example.com", shopBackend, shopRoutes()); err != nil {
		t.Fatalf("Route = %v", err)
	}

	matcher := server.current().PathMatchers[0]
	if want := "projects/acme-prod/global/backendServices/" + shopBackend; matcher.DefaultService != want {
		t.Errorf("the path matcher defaults to %q, want %q: everything outside the immutable prefixes still reaches Cloud Run", matcher.DefaultService, want)
	}
	if len(matcher.RouteRules) != 1 {
		t.Fatalf("the path matcher has route rules %+v, want one for the immutable prefix", matcher.RouteRules)
	}
	rule := matcher.RouteRules[0]
	if want := "projects/acme-prod/global/backendBuckets/ocel-alb-production-assets"; rule.Service != want {
		t.Errorf("the rule serves %q, want %q", rule.Service, want)
	}
	if len(rule.MatchRules) != 1 || rule.MatchRules[0].PrefixMatch != "/_next/static/" {
		t.Errorf("the rule matches %+v, want the prefix /_next/static/", rule.MatchRules)
	}
	if rule.RouteAction == nil || rule.RouteAction.UrlRewrite == nil ||
		rule.RouteAction.UrlRewrite.PathPrefixRewrite != "/assets/production/shop/web/r1/assets/_next/static/" {
		t.Errorf("the rule rewrites with %+v, want the matched prefix replaced so the request path lands on the release's object key", rule.RouteAction)
	}
}

func TestAMustRevalidatePrefixUnderAnImmutableOneStaysWithCloudRun(t *testing.T) {
	t.Parallel()

	routes := shopRoutes()
	routes.Revalidate = []string{"/_next/static/media/live/", "/elsewhere/"}
	p, server := routing(t, emptyMap())
	if err := p.Route(context.Background(), tierRoutes, "shop.example.com", shopBackend, routes); err != nil {
		t.Fatalf("Route = %v", err)
	}

	rules := server.current().PathMatchers[0].RouteRules
	if len(rules) != 2 {
		t.Fatalf("the path matcher has route rules %+v, want the revalidating prefix under the immutable one, and that one", rules)
	}
	first := rules[0]
	if first.MatchRules[0].PrefixMatch != "/_next/static/media/live/" || first.Service != "projects/acme-prod/global/backendServices/"+shopBackend || first.RouteAction != nil {
		t.Errorf("the first rule is %+v, want the revalidating prefix kept on the backend service with no rewrite", first)
	}
	if first.Priority >= rules[1].Priority {
		t.Errorf("the revalidating rule has priority %d and the immutable one %d, want it examined first", first.Priority, rules[1].Priority)
	}
}

func TestRoutingAgainWithAnotherReleasesStaticRoutesRewritesTheRuleToThatRelease(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p, server := routing(t, emptyMap())
	if err := p.Route(ctx, tierRoutes, "shop.example.com", shopBackend, shopRoutes()); err != nil {
		t.Fatalf("Route = %v", err)
	}
	next := shopRoutes()
	next.Objects = "/assets/production/shop/web/r2/assets"
	if err := p.Route(ctx, tierRoutes, "shop.example.com", shopBackend, next); err != nil {
		t.Fatalf("Route = %v", err)
	}

	got := server.current().PathMatchers[0].RouteRules[0].RouteAction.UrlRewrite.PathPrefixRewrite
	if want := "/assets/production/shop/web/r2/assets/_next/static/"; got != want {
		t.Errorf("the rule rewrites to %q after the promote, want %q", got, want)
	}
	if got := server.writes(); got != 2 {
		t.Errorf("two routes to different releases wrote %d times, want 2", got)
	}
}

func TestRoutingTheSameStaticRoutesAgainWritesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p, server := routing(t, emptyMap())
	for range 2 {
		if err := p.Route(ctx, tierRoutes, "shop.example.com", shopBackend, shopRoutes()); err != nil {
			t.Fatalf("Route = %v", err)
		}
	}
	if got := server.writes(); got != 1 {
		t.Errorf("routing identical static routes twice wrote %d times, want 1", got)
	}
}

func TestRoutingWithoutStaticRoutesDropsTheRulesAReleaseLeft(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p, server := routing(t, emptyMap())
	if err := p.Route(ctx, tierRoutes, "shop.example.com", shopBackend, shopRoutes()); err != nil {
		t.Fatalf("Route = %v", err)
	}
	if err := p.Route(ctx, tierRoutes, "shop.example.com", shopBackend, nil); err != nil {
		t.Fatalf("Route = %v", err)
	}
	if rules := server.current().PathMatchers[0].RouteRules; len(rules) != 0 {
		t.Errorf("the path matcher keeps route rules %+v for a release with no static files", rules)
	}
}

func TestRoutingTheSameStaticRoutesAgainWritesNothingWhenComputeAnswersWithFullURLs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p, server := routing(t, emptyMap())
	server.answersFullURLs()
	for range 2 {
		if err := p.Route(ctx, tierRoutes, "shop.example.com", shopBackend, shopRoutes()); err != nil {
			t.Fatalf("Route = %v", err)
		}
	}
	if got := server.writes(); got != 1 {
		t.Errorf("routing identical static routes twice wrote %d times, want 1: Compute names a backend by its full URL, and a reach must not rewrite the url map for it", got)
	}
}
