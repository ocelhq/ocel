package gcp

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func TestTheLoadBalancerBehindTheALBEdgeIsAPartNoBootstrapRequires(t *testing.T) {
	t.Parallel()

	catalogue := bootstrap{}.Catalogue()
	for _, kind := range []edge.Kind{alb.Kind, edge.None} {
		required, err := bootstrapplan.RequiredFeatures(catalogue, kind)
		if err != nil {
			t.Fatalf("RequiredFeatures(%s) = %v", kind, err)
		}
		if len(required) != 0 {
			t.Errorf("a bootstrap fronted by %q requires %v, want nothing: the first deploy behind the alb adds its load balancer", kind, required)
		}
	}
	b := bootstrap{clients: &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1"}}
	balancer := b.albPart(environment.TierProduction)
	if !strings.Contains(balancer.summary, "$18") || !balancer.retires {
		t.Errorf("the %s part is %+v, want its summary to say the price and it retiring with its last project", albPart, balancer)
	}
	for _, want := range []string{"gate", "plugin", "repository"} {
		if !strings.Contains(balancer.summary, want) {
			t.Errorf("the %s part reads %q, want it to mention the %s it holds", albPart, balancer.summary, want)
		}
	}
}

func TestTheShieldedLoadBalancerIsAFeatureOnlyTheCloudflareEdgePullsIn(t *testing.T) {
	t.Parallel()

	catalogue := bootstrap{}.Catalogue()
	at := slices.IndexFunc(catalogue, func(f provider.Feature) bool { return f.Name == "cloudflare-origin" })
	if at < 0 {
		t.Fatalf("Catalogue() = %v, want the %q feature: the load balancer Cloudflare forwards to costs every month, and a recurring cost is consented to by being planned", catalogue, albShieldedFeature)
	}
	if !strings.Contains(catalogue[at].Summary, "$18") {
		t.Errorf("the %q feature reads %q, want the price in the plan", albShieldedFeature, catalogue[at].Summary)
	}
	proxied, err := bootstrapplan.RequiredFeatures(catalogue, "cloudflare")
	if err != nil {
		t.Fatalf("RequiredFeatures(cloudflare) = %v", err)
	}
	if !slices.Contains(proxied, albShieldedFeature) {
		t.Errorf("a cloudflare bootstrap requires %v, want %q and not the front browsers reach directly", proxied, albShieldedFeature)
	}
	fronted, err := bootstrapplan.RequiredFeatures(catalogue, alb.Kind)
	if err != nil {
		t.Fatalf("RequiredFeatures(alb) = %v", err)
	}
	if slices.Contains(fronted, albShieldedFeature) {
		t.Errorf("an %q bootstrap requires %v, and a load balancer only Cloudflare reaches serves nothing it binds", alb.Kind, fronted)
	}

	for _, api := range []string{"compute.googleapis.com", "certificatemanager.googleapis.com", "networksecurity.googleapis.com"} {
		if !slices.Contains(apisFor(environment.TierProduction, []string{albShieldedFeature}), api) {
			t.Errorf("a %q bootstrap checks %v, want %s among them", albShieldedFeature, apisFor(environment.TierProduction, []string{albShieldedFeature}), api)
		}
	}
	if !slices.Contains(permissionsFor([]string{albShieldedFeature}), "networksecurity.serverTlsPolicies.create") {
		t.Errorf("a %q bootstrap checks %v, want the permission its server tls policy is raised with", albShieldedFeature, permissionsFor([]string{albShieldedFeature}))
	}
	if !slices.Contains(rolesCovering([]string{albShieldedFeature}), "roles/compute.securityAdmin") {
		t.Errorf("the refusal for a %q bootstrap names %v, want the role that covers its server tls policy", albShieldedFeature, rolesCovering([]string{albShieldedFeature}))
	}
}

func TestTheServicesTheLoadBalancerNeedsAreOnlyDemandedWhenItIsBeingAdded(t *testing.T) {
	t.Parallel()

	b := bootstrap{clients: &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1"}}
	balancer := b.albPart(environment.TierPreview)
	for _, api := range []string{"compute.googleapis.com", "certificatemanager.googleapis.com", "networkservices.googleapis.com"} {
		if !slices.Contains(balancer.apis, api) {
			t.Errorf("the %s part checks %v, want %s among them: the gate is a Service Extensions plugin, and the front needs the rest", albPart, balancer.apis, api)
		}
	}
	plain := apisFor(environment.TierProduction, nil)
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
	if slices.Contains(apisFor(environment.TierPreview, []string{albShieldedFeature}), "networkservices.googleapis.com") {
		t.Error("a bootstrap for the load balancer only Cloudflare reaches demands the Service Extensions API, and the worker in front of it gates")
	}
}

func TestABootstrapProvisioningNoLoadBalancerChecksNoLoadBalancerPermission(t *testing.T) {
	t.Parallel()

	for _, permission := range permissionsFor(nil) {
		if strings.HasPrefix(permission, "compute.") || strings.HasPrefix(permission, "certificatemanager.") {
			t.Errorf("a bootstrap provisioning no load balancer checks %s, and a credential would be refused for a service it never calls", permission)
		}
	}
	if slices.ContainsFunc(rolesCovering(nil), func(role string) bool {
		return role == "roles/compute.loadBalancerAdmin" || strings.HasPrefix(role, "roles/certificatemanager.")
	}) {
		t.Errorf("the refusal for a plain bootstrap names %v, and a role for a front nothing provisions is one more than the credential needs", rolesCovering(nil))
	}
}
