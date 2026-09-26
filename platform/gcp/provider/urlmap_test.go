package gcp

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"google.golang.org/api/compute/v1"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

const classRoutes = "ocel-alb-production-routes"

func emptyMap() *compute.UrlMap {
	return &compute.UrlMap{Name: classRoutes, Fingerprint: "0", DefaultService: "notfound"}
}

func TestRoutingAHostnameWritesTheHostRuleAndThePathMatcherThatServesIt(t *testing.T) {
	t.Parallel()

	p, server := routing(t, emptyMap())
	if err := p.Route(context.Background(), classRoutes, "shop.example.com", "ocel-alb-shop-production-shop"); err != nil {
		t.Fatalf("Route = %v", err)
	}

	urlMap := server.current()
	if len(urlMap.HostRules) != 1 || urlMap.HostRules[0].Hosts[0] != "shop.example.com" {
		t.Fatalf("the url map has host rules %+v, want one for shop.example.com", urlMap.HostRules)
	}
	if len(urlMap.PathMatchers) != 1 || urlMap.HostRules[0].PathMatcher != urlMap.PathMatchers[0].Name {
		t.Fatalf("the url map has path matchers %+v, want the one the host rule names", urlMap.PathMatchers)
	}
	want := "projects/acme-prod/global/backendServices/ocel-alb-shop-production-shop"
	if got := urlMap.PathMatchers[0].DefaultService; got != want {
		t.Errorf("the path matcher serves %q, want %q: a backend is named by its full path in a url map", got, want)
	}
}

func TestUnroutingTheLastHostnameClearsTheRuleRatherThanLeavingItPointingAtADeletedBackend(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p, server := routing(t, emptyMap())
	if err := p.Route(ctx, classRoutes, "shop.example.com", "ocel-alb-shop-production-shop"); err != nil {
		t.Fatalf("Route = %v", err)
	}
	if err := p.Unroute(ctx, classRoutes, "shop.example.com"); err != nil {
		t.Fatalf("Unroute = %v", err)
	}

	urlMap := server.current()
	if len(urlMap.HostRules) != 0 || len(urlMap.PathMatchers) != 0 {
		t.Errorf("the url map still has %+v and %+v after the only hostname was released, and the binding stack deletes the backend they name",
			urlMap.HostRules, urlMap.PathMatchers)
	}
}

func TestRoutingRetriesTheWriteTheUrlMapChangedUnder(t *testing.T) {
	t.Parallel()

	p, server := routing(t, emptyMap())
	server.conflicts = 1
	if err := p.Route(context.Background(), classRoutes, "shop.example.com", "ocel-alb-shop-production-shop"); err != nil {
		t.Fatalf("Route = %v", err)
	}

	if got := server.writes(); got != 2 {
		t.Errorf("the route wrote %d times, want 2: the url map is shared by every project in the class, so a stale fingerprint is re-read and written again", got)
	}
	if len(server.current().HostRules) != 1 {
		t.Errorf("the url map has %+v after the retry, want the host rule the route asked for", server.current().HostRules)
	}
}

func TestAHostnameServedNotFoundIsAnsweredWithA404RatherThanTheEmptyBackendsGatewayError(t *testing.T) {
	t.Parallel()

	p, server := routing(t, emptyMap())
	if err := p.ServeNotFound(context.Background(), classRoutes, "shop.example.com"); err != nil {
		t.Fatalf("ServeNotFound = %v", err)
	}

	urlMap := server.current()
	if len(urlMap.PathMatchers) != 1 {
		t.Fatalf("the url map has path matchers %+v, want the one that answers a hostname whose app has released nothing", urlMap.PathMatchers)
	}
	matcher := urlMap.PathMatchers[0]
	if matcher.DefaultService != urlMap.DefaultService {
		t.Errorf("the path matcher serves %q, want the map's own default %q: a path matcher with no service at all is refused",
			matcher.DefaultService, urlMap.DefaultService)
	}
	abort := notFoundAbort(matcher.DefaultRouteAction)
	if abort == nil || abort.HttpStatus != http.StatusNotFound || abort.Percentage != 100 {
		t.Errorf("the path matcher aborts with %+v, want every request answered 404: the not-found backend has no backends, "+
			"so anything reaching it is answered 502 instead of refused", abort)
	}
}

func TestANotFoundHostnameFollowsTheMapsDefaultWhenTheFrontIsRaisedAgain(t *testing.T) {
	t.Parallel()

	stale := &compute.UrlMap{
		Name: classRoutes, Fingerprint: "0", DefaultService: "notfound-v2",
		HostRules:    []*compute.HostRule{{Hosts: []string{"unreleased.example.com"}, PathMatcher: matcherFor("unreleased.example.com")}},
		PathMatchers: []*compute.PathMatcher{{Name: matcherFor("unreleased.example.com"), DefaultService: "notfound-v1", DefaultRouteAction: refusing()}},
	}
	p, server := routing(t, stale)
	if err := p.Route(context.Background(), classRoutes, "shop.example.com", "ocel-alb-shop-production-shop"); err != nil {
		t.Fatalf("Route = %v", err)
	}

	urlMap := server.current()
	for _, matcher := range urlMap.PathMatchers {
		if notFoundAbort(matcher.DefaultRouteAction) != nil && matcher.DefaultService != urlMap.DefaultService {
			t.Errorf("the 404 matcher still serves %q while the map defaults to %q: a backend the front no longer provisions makes the whole url map invalid",
				matcher.DefaultService, urlMap.DefaultService)
		}
	}
}

func notFoundAbort(action *compute.HttpRouteAction) *compute.HttpFaultAbort {
	if action == nil || action.FaultInjectionPolicy == nil {
		return nil
	}
	return action.FaultInjectionPolicy.Abort
}

func TestRoutingRefusesAHostRuleThatNamesNoBackend(t *testing.T) {
	t.Parallel()

	p, _ := routing(t, emptyMap())
	err := p.Route(context.Background(), classRoutes, "shop.example.com", "")
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Route with no backend = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
}
