package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/direct"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func TestTheLoadBalancerIsAFeatureOnlyTheEdgeThatNeedsItPullsIn(t *testing.T) {
	t.Parallel()

	catalogue := bootstrap{}.Catalogue()
	at := slices.IndexFunc(catalogue, func(f provider.Feature) bool { return f.Name == albFeature })
	if at < 0 {
		t.Fatalf("Catalogue() = %v, want the %q feature: a standing cost is consented to by being planned", catalogue, albFeature)
	}
	feature := catalogue[at]
	if !strings.Contains(feature.Summary, "$18") {
		t.Errorf("the %q feature reads %q, and the one bootstrap item with a standing cost says the price in the plan", albFeature, feature.Summary)
	}

	fronted, err := bootstrapplan.RequiredFeatures(catalogue, nil, string(alb.Kind))
	if err != nil {
		t.Fatalf("RequiredFeatures(alb) = %v", err)
	}
	if !slices.Contains(fronted, albFeature) {
		t.Errorf("an %q bootstrap requires %v, want %q among them", alb.Kind, fronted, albFeature)
	}

	plain, err := bootstrapplan.RequiredFeatures(catalogue, nil, string(direct.Kind))
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

func TestAnAlbBootstrapChecksTheComputeAndCertificateManagerPermissionsAndNamesTheRolesThatCoverThem(t *testing.T) {
	t.Parallel()

	fronted := permissionsFor([]string{albFeature})
	for _, permission := range []string{
		"compute.urlMaps.update", "compute.regionNetworkEndpointGroups.create", "certificatemanager.certmapentries.create",
	} {
		if !slices.Contains(fronted, permission) {
			t.Errorf("an %q bootstrap checks %v, want %s among them: the front is raised with it, and a hostname is bound with it", alb.Kind, fronted, permission)
		}
	}
	for _, permission := range bootstrapPermissions {
		if !slices.Contains(fronted, permission) {
			t.Errorf("an %q bootstrap no longer checks %s", alb.Kind, permission)
		}
	}
	plain := permissionsFor(nil)
	for _, permission := range plain {
		if strings.HasPrefix(permission, "compute.") || strings.HasPrefix(permission, "certificatemanager.") {
			t.Errorf("a bootstrap standing no load balancer up checks %s, and a credential would be refused for a service it never calls", permission)
		}
	}

	roles := rolesCovering([]string{albFeature})
	for _, role := range []string{"roles/compute.loadBalancerAdmin", "roles/certificatemanager.owner"} {
		if !slices.Contains(roles, role) {
			t.Errorf("the refusal for an %q bootstrap names %v, want %s among them so the reader knows what to grant", alb.Kind, roles, role)
		}
	}
	if slices.ContainsFunc(rolesCovering(nil), func(role string) bool {
		return strings.HasPrefix(role, "roles/compute.") || strings.HasPrefix(role, "roles/certificatemanager.")
	}) {
		t.Errorf("the refusal for a plain bootstrap names %v, and a role for a front nothing stands up is one more than the credential needs", rolesCovering(nil))
	}
}

func TestThePlanNamesTheLoadBalancerGroupOnlyForTheEdgeThatStandsItUp(t *testing.T) {
	t.Parallel()

	b := bootstrap{}
	catalogue := b.Catalogue()
	read, err := b.described(context.Background(),
		survey{Names: Names{namespace: "ocel", project: "acme-prod"}, Class: edge.ClassProduction, Project: "acme-prod"})
	if err != nil {
		t.Fatalf("described = %v", err)
	}

	fronted := bootstrapplan.ChangeGroups(read, catalogue, provider.BootstrapRequest{
		Class: edge.ClassProduction, Features: []string{albFeature},
	})
	if !slices.ContainsFunc(fronted, func(g provider.ChangeGroup) bool { return g.Feature == albFeature }) {
		t.Errorf("an %q plan holds %v, want a group for %q so the reader sees what it costs before it stands", alb.Kind, fronted, albFeature)
	}

	plain := bootstrapplan.ChangeGroups(read, catalogue, provider.BootstrapRequest{Class: edge.ClassProduction})
	if slices.ContainsFunc(plain, func(g provider.ChangeGroup) bool { return g.Feature == albFeature }) {
		t.Errorf("a plan that asked for no edge holds %v, and a load balancer nothing named would stand and bill", plain)
	}
}
