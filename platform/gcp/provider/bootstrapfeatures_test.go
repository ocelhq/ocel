package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func TestTheLoadBalancerIsAFeatureOnlyTheEdgeThatNeedsItPullsIn(t *testing.T) {
	t.Parallel()

	catalogue := bootstrap{}.Catalogue()
	at := slices.IndexFunc(catalogue, func(f provider.Feature) bool { return f.Name == albFeature })
	if at < 0 {
		t.Fatalf("Catalogue() = %v, want the %q feature: a recurring cost is consented to by being planned", catalogue, albFeature)
	}
	feature := catalogue[at]
	if !strings.Contains(feature.Summary, "$18") {
		t.Errorf("the %q feature reads %q, and the one bootstrap item with a recurring cost says the price in the plan", albFeature, feature.Summary)
	}

	fronted, err := bootstrapplan.RequiredFeatures(catalogue, nil, alb.Kind)
	if err != nil {
		t.Fatalf("RequiredFeatures(alb) = %v", err)
	}
	if !slices.Contains(fronted, albFeature) {
		t.Errorf("an %q bootstrap requires %v, want %q among them", alb.Kind, fronted, albFeature)
	}

	plain, err := bootstrapplan.RequiredFeatures(catalogue, nil, edge.None)
	if err != nil {
		t.Fatalf("RequiredFeatures(no edge) = %v", err)
	}
	if slices.Contains(plain, albFeature) {
		t.Errorf("a bootstrap with no edge requires %v, and nothing is defaulted onto a forwarding rule that bills whether it serves a request or not",
			plain)
	}
}

func TestTheShieldedLoadBalancerIsAFeatureOnlyTheCloudflareEdgePullsIn(t *testing.T) {
	t.Parallel()

	catalogue := bootstrap{}.Catalogue()
	at := slices.IndexFunc(catalogue, func(f provider.Feature) bool { return f.Name == albShieldedFeature })
	if at < 0 {
		t.Fatalf("Catalogue() = %v, want the %q feature: the load balancer Cloudflare forwards to costs every month, and a recurring cost is consented to by being planned", catalogue, albShieldedFeature)
	}
	if !strings.Contains(catalogue[at].Summary, "$18") {
		t.Errorf("the %q feature reads %q, want the price in the plan", albShieldedFeature, catalogue[at].Summary)
	}
	proxied, err := bootstrapplan.RequiredFeatures(catalogue, nil, "cloudflare")
	if err != nil {
		t.Fatalf("RequiredFeatures(cloudflare) = %v", err)
	}
	if !slices.Contains(proxied, albShieldedFeature) || slices.Contains(proxied, albFeature) {
		t.Errorf("a cloudflare bootstrap requires %v, want %q and not the front browsers reach directly", proxied, albShieldedFeature)
	}
	fronted, err := bootstrapplan.RequiredFeatures(catalogue, nil, alb.Kind)
	if err != nil {
		t.Fatalf("RequiredFeatures(alb) = %v", err)
	}
	if slices.Contains(fronted, albShieldedFeature) {
		t.Errorf("an %q bootstrap requires %v, and a load balancer only Cloudflare reaches serves nothing it binds", alb.Kind, fronted)
	}

	for _, api := range []string{"compute.googleapis.com", "certificatemanager.googleapis.com", "networksecurity.googleapis.com"} {
		if !slices.Contains(apisFor([]string{albShieldedFeature}), api) {
			t.Errorf("a %q bootstrap checks %v, want %s among them", albShieldedFeature, apisFor([]string{albShieldedFeature}), api)
		}
	}
	if !slices.Contains(permissionsFor([]string{albShieldedFeature}), "networksecurity.serverTlsPolicies.create") {
		t.Errorf("a %q bootstrap checks %v, want the permission its server tls policy is raised with", albShieldedFeature, permissionsFor([]string{albShieldedFeature}))
	}
	if !slices.Contains(rolesCovering([]string{albShieldedFeature}), "roles/compute.securityAdmin") {
		t.Errorf("the refusal for a %q bootstrap names %v, want the role that covers its server tls policy", albShieldedFeature, rolesCovering([]string{albShieldedFeature}))
	}
}

func TestTheServicesTheLoadBalancerNeedsAreOnlyDemandedWhenItIsBeingProvisioned(t *testing.T) {
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
			t.Errorf("a bootstrap provisioning no load balancer demands %s, and it would be refused for a service it never calls", api)
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
			t.Errorf("a bootstrap provisioning no load balancer checks %s, and a credential would be refused for a service it never calls", permission)
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
		t.Errorf("the refusal for a plain bootstrap names %v, and a role for a front nothing provisions is one more than the credential needs", rolesCovering(nil))
	}
}

func TestThePlanNamesTheLoadBalancerGroupOnlyForTheEdgeThatNeedsIt(t *testing.T) {
	t.Parallel()

	b := bootstrap{}
	catalogue := b.Catalogue()
	read, err := b.described(context.Background(),
		survey{Names: Names{namespace: "ocel", project: "acme-prod"}, Tier: environment.TierProduction, Project: "acme-prod"})
	if err != nil {
		t.Fatalf("described = %v", err)
	}

	fronted := bootstrapplan.ChangeGroups(read, catalogue, provider.BootstrapRequest{
		Tier: environment.TierProduction, Features: []string{albFeature},
	})
	if !slices.ContainsFunc(fronted, func(g provider.ChangeGroup) bool { return g.Feature == albFeature }) {
		t.Errorf("an %q plan has %v, want a group for %q so the reader sees what it costs before it is provisioned", alb.Kind, fronted, albFeature)
	}

	plain := bootstrapplan.ChangeGroups(read, catalogue, provider.BootstrapRequest{Tier: environment.TierProduction})
	if slices.ContainsFunc(plain, func(g provider.ChangeGroup) bool { return g.Feature == albFeature }) {
		t.Errorf("a plan that asked for no edge has %v, and a load balancer nothing named would be provisioned and bill", plain)
	}
}
