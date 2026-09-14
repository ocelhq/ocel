package alb

import (
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestTheAlbEdgeDocumentsTheCertificateManagerAndComputeRolesEachTierNeeds(t *testing.T) {
	t.Parallel()

	front, _ := fronting(t)
	for _, tier := range []edge.CredentialTier{edge.TierBootstrap, edge.TierDeploy} {
		documented, err := front.CredentialPermissions(tier)
		if err != nil {
			t.Fatalf("CredentialPermissions(%s) = %v", tier, err)
		}
		for _, role := range []string{"roles/compute.loadBalancerAdmin", "roles/certificatemanager.owner"} {
			if !strings.Contains(documented.Document, role) {
				t.Errorf("the %s tier's document reads %q, want %s in it: the front is compute resources and a certificate map, and binding a hostname writes into both",
					tier, documented.Document, role)
			}
		}
		if !strings.Contains(documented.Heading, "acme-prod") {
			t.Errorf("the %s tier's heading reads %q, want the project the roles are granted on", tier, documented.Heading)
		}
	}
	if _, err := front.CredentialPermissions(edge.CredentialTier("runtime")); err == nil {
		t.Error("CredentialPermissions(runtime) rendered a document for a tier nothing defines")
	}
}
