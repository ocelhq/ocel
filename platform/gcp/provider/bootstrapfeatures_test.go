package gcp

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/direct"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func TestTheLoadBalancerIsAFeatureOnlyTheEdgeThatNeedsItPullsIn(t *testing.T) {
	t.Parallel()

	catalogue := bootstrapper{}.Catalogue()
	at := slices.IndexFunc(catalogue, func(f providerkit.Feature) bool { return f.Name == albFeature })
	if at < 0 {
		t.Fatalf("Catalogue() = %v, want the %q feature: a standing cost is consented to by being planned", catalogue, albFeature)
	}
	feature := catalogue[at]
	if !strings.Contains(feature.Summary, "$18") {
		t.Errorf("the %q feature reads %q, and the one bootstrap item with a standing cost says the price in the plan", albFeature, feature.Summary)
	}

	fronted, err := providerkit.RequiredFeatures(catalogue, nil, string(alb.Kind))
	if err != nil {
		t.Fatalf("RequiredFeatures(alb) = %v", err)
	}
	if !slices.Contains(fronted, albFeature) {
		t.Errorf("an %q bootstrap requires %v, want %q among them", alb.Kind, fronted, albFeature)
	}

	plain, err := providerkit.RequiredFeatures(catalogue, nil, string(direct.Kind))
	if err != nil {
		t.Fatalf("RequiredFeatures(direct) = %v", err)
	}
	if slices.Contains(plain, albFeature) {
		t.Errorf("a %q bootstrap requires %v, and nothing is defaulted onto a forwarding rule that bills whether it serves a request or not",
			direct.Kind, plain)
	}
}

func TestTheServicesTheLoadBalancerNeedsAreOnlyDemandedWhenItIsBeingStoodUp(t *testing.T) {
	t.Parallel()

	fronted := apisFor([]string{albFeature})
	for _, api := range []string{"compute.googleapis.com", "certificatemanager.googleapis.com"} {
		if !slices.Contains(fronted, api) {
			t.Errorf("an %q bootstrap checks %v, want %s among them: the apply would fail on the first resource that needs it",
				alb.Kind, fronted, api)
		}
	}
	plain := apisFor(nil)
	for _, api := range []string{"compute.googleapis.com", "certificatemanager.googleapis.com"} {
		if slices.Contains(plain, api) {
			t.Errorf("a bootstrap standing no load balancer up demands %s, and it would be refused for a service it never calls", api)
		}
	}
	for _, api := range BootstrapAPIs {
		if !slices.Contains(plain, api) {
			t.Errorf("a bootstrap no longer checks %s, and the apply would fail on the first resource that needs it", api)
		}
	}
}

func TestThePlanNamesTheLoadBalancerGroupOnlyForTheEdgeThatStandsItUp(t *testing.T) {
	t.Parallel()

	read := survey{Names: Names{namespace: "ocel", project: "acme-prod"}, Class: providerkit.ClassProduction, Project: "acme-prod"}
	catalogue := bootstrapper{}.Catalogue()

	fronted := providerkit.DeriveGroups(described(read), catalogue, providerkit.BootstrapRequest{
		Class: providerkit.ClassProduction, Features: []string{albFeature},
	})
	if !slices.ContainsFunc(fronted, func(g providerkit.ChangeGroup) bool { return g.Feature == albFeature }) {
		t.Errorf("an %q plan holds %v, want a group for %q so the reader sees what it costs before it stands", alb.Kind, fronted, albFeature)
	}

	plain := providerkit.DeriveGroups(described(read), catalogue, providerkit.BootstrapRequest{Class: providerkit.ClassProduction})
	if slices.ContainsFunc(plain, func(g providerkit.ChangeGroup) bool { return g.Feature == albFeature }) {
		t.Errorf("a plan that asked for no edge holds %v, and a load balancer nothing named would stand and bill", plain)
	}
}
