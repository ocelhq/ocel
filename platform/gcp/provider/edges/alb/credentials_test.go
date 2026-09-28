package alb

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
)

func TestTheAlbEdgeDocumentsTheCertificateManagerAndComputeRolesEachPurposeNeeds(t *testing.T) {
	t.Parallel()

	front, _ := fronting(t)
	for _, purpose := range []edge.CredentialPurpose{edge.PurposeBootstrap, edge.PurposeDeploy} {
		documented, err := front.Hooks().DescribeCredentialPermissions(purpose)
		if err != nil {
			t.Fatalf("CredentialPermissions(%s) = %v", purpose, err)
		}
		for _, role := range []string{"roles/compute.loadBalancerAdmin", "roles/certificatemanager.owner"} {
			if !strings.Contains(documented.Document, role) {
				t.Errorf("the %s purpose's document reads %q, want %s in it: the front is compute resources and a certificate map, and binding a hostname writes into both",
					purpose, documented.Document, role)
			}
		}
		if !strings.Contains(documented.Heading, "acme-prod") {
			t.Errorf("the %s purpose's heading reads %q, want the project the roles are granted on", purpose, documented.Heading)
		}
	}
	if _, err := front.Hooks().DescribeCredentialPermissions(edge.CredentialPurpose("runtime")); err == nil {
		t.Error("CredentialPermissions(runtime) rendered a document for a purpose nothing defines")
	}
}
