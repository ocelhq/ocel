package alb

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
)

func TestTheAlbEdgeDocumentsTheCertificateManagerAndComputeRolesEachPurposeNeeds(t *testing.T) {
	t.Parallel()

	balancer, _ := balancing(t)
	for _, purpose := range []edge.CredentialPurpose{edge.PurposeBootstrap, edge.PurposeDeploy} {
		documented, err := balancer.Hooks().DescribeCredentialPermissions(purpose)
		if err != nil {
			t.Fatalf("CredentialPermissions(%s) = %v", purpose, err)
		}
		for _, role := range []string{"roles/compute.loadBalancerAdmin", "roles/certificatemanager.owner"} {
			if !strings.Contains(documented.Document, role) {
				t.Errorf("the %s purpose's document reads %q, want %s in it: the balancer is compute resources and a certificate map, and binding a hostname writes into both",
					purpose, documented.Document, role)
			}
		}
		if !strings.Contains(documented.Heading, "acme-prod") {
			t.Errorf("the %s purpose's heading reads %q, want the project the roles are granted on", purpose, documented.Heading)
		}
	}
	if _, err := balancer.Hooks().DescribeCredentialPermissions(edge.CredentialPurpose("runtime")); err == nil {
		t.Error("CredentialPermissions(runtime) rendered a document for a purpose nothing defines")
	}
}

func TestABootstrapOfTheLoadBalancerChecksItMayRaiseTheBackendBucketTheTiersAssetsAreServedFrom(t *testing.T) {
	t.Parallel()

	for _, permission := range []string{"compute.backendBuckets.create", "compute.backendBuckets.delete", "compute.backendBuckets.update", "compute.backendBuckets.use"} {
		if !slices.Contains(Permissions, permission) {
			t.Errorf("the load balancer's preflight checks %v, want %s: the balancer serves immutable files from a backend bucket and a url map names it", Permissions, permission)
		}
	}
}

func TestABootstrapOfTheShieldedLoadBalancerChecksItMayCreateThePlainHTTPRedirect(t *testing.T) {
	t.Parallel()

	for _, permission := range []string{"compute.targetHttpProxies.create", "compute.targetHttpProxies.delete"} {
		if !slices.Contains(ShieldedPermissions, permission) {
			t.Errorf("the shielded load balancer's preflight checks %v, want %s: it answers plain http with a redirect through a target http proxy", ShieldedPermissions, permission)
		}
	}
}

func TestABootstrapOfTheLoadBalancerChecksItMayAttachTheGatePlugin(t *testing.T) {
	t.Parallel()

	for _, permission := range []string{
		"networkservices.wasmPlugins.create", "networkservices.wasmPlugins.update", "networkservices.wasmPlugins.delete", "networkservices.wasmPlugins.get",
		"networkservices.lbEdgeExtensions.create", "networkservices.lbEdgeExtensions.update", "networkservices.lbEdgeExtensions.delete", "networkservices.lbEdgeExtensions.get",
		"networkservices.operations.get",
	} {
		if !slices.Contains(Permissions, permission) {
			t.Errorf("the load balancer's preflight checks %v, want %s: the preview tier runs its gate as a plugin on a load balancer edge extension", Permissions, permission)
		}
	}
	if !slices.Contains(Roles(), "roles/networkservices.admin") {
		t.Errorf("Roles() = %v, want roles/networkservices.admin: Google names it for Service Extensions", Roles())
	}
}
