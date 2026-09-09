package gcp

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/api/compute/v1"

	"github.com/ocelhq/ocel/pkg/providerkit"
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

	held := server.standing()
	if len(held.HostRules) != 1 || held.HostRules[0].Hosts[0] != "shop.example.com" {
		t.Fatalf("the url map holds host rules %+v, want one for shop.example.com", held.HostRules)
	}
	if len(held.PathMatchers) != 1 || held.HostRules[0].PathMatcher != held.PathMatchers[0].Name {
		t.Fatalf("the url map holds path matchers %+v, want the one the host rule names", held.PathMatchers)
	}
	want := "projects/acme-prod/global/backendServices/ocel-alb-shop-production-shop"
	if got := held.PathMatchers[0].DefaultService; got != want {
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

	held := server.standing()
	if len(held.HostRules) != 0 || len(held.PathMatchers) != 0 {
		t.Errorf("the url map still holds %+v and %+v after the only hostname was released, and the binding stack deletes the backend they name",
			held.HostRules, held.PathMatchers)
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
	if len(server.standing().HostRules) != 1 {
		t.Errorf("the url map holds %+v after the retry, want the host rule the route asked for", server.standing().HostRules)
	}
}

func TestRoutingRefusesAHostRuleThatNamesNoBackend(t *testing.T) {
	t.Parallel()

	p, _ := routing(t, emptyMap())
	err := p.Route(context.Background(), classRoutes, "shop.example.com", "")
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Route with no backend = %v, want an %s refusal", err, providerkit.CodeInvalid)
	}
}
