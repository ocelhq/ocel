package alb

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	loadBalancerRole = "roles/compute.loadBalancerAdmin"
	certificatesRole = "roles/certificatemanager.owner"
)

var roles = []string{loadBalancerRole, certificatesRole}

func Roles() []string { return slices.Clone(roles) }

var shieldingRoles = []string{"roles/compute.networkAdmin", "roles/compute.securityAdmin"}

func ShieldedRoles() []string { return slices.Concat(roles, shieldingRoles) }

var ShieldedPermissions = slices.Concat(Permissions, []string{
	"certificatemanager.trustconfigs.create",
	"certificatemanager.trustconfigs.delete",
	"certificatemanager.trustconfigs.get",
	"certificatemanager.trustconfigs.update",
	"certificatemanager.trustconfigs.use",
	"networksecurity.serverTlsPolicies.create",
	"networksecurity.serverTlsPolicies.delete",
	"networksecurity.serverTlsPolicies.get",
	"networksecurity.serverTlsPolicies.use",
	"compute.targetHttpsProxies.update",
})

var Permissions = []string{
	"compute.globalAddresses.create",
	"compute.globalAddresses.delete",
	"compute.backendServices.create",
	"compute.backendServices.delete",
	"compute.backendServices.update",
	"compute.urlMaps.create",
	"compute.urlMaps.delete",
	"compute.urlMaps.update",
	"compute.targetHttpsProxies.create",
	"compute.targetHttpsProxies.delete",
	"compute.globalForwardingRules.create",
	"compute.globalForwardingRules.delete",
	"compute.regionNetworkEndpointGroups.create",
	"compute.regionNetworkEndpointGroups.delete",
	"compute.globalOperations.get",
	"certificatemanager.certmaps.create",
	"certificatemanager.certmaps.delete",
	"certificatemanager.certmaps.get",
	"certificatemanager.certmapentries.create",
	"certificatemanager.certmapentries.delete",
	"certificatemanager.certmapentries.list",
	"certificatemanager.operations.get",
}

func (e *Edge) credentialPermissions(purpose edge.CredentialPurpose) (edge.CredentialDocument, error) {
	var does string
	switch purpose {
	case edge.PurposeBootstrap:
		does = "provisions the load balancer and takes it down"
	case edge.PurposeDeploy:
		does = "binds a hostname: a certificate and its map entry, a backend onto a serverless NEG, and a host rule in the url map"
	default:
		return edge.CredentialDocument{}, refusal.Refuse(refusal.CodeInvalid,
			"the %s edge lists its roles for bootstrap or deploy credentials, not %q", Kind, string(purpose))
	}
	return edge.CredentialDocument{
		Heading:  fmt.Sprintf("the roles the %s edge adds on project %s, since a %s credential %s", Kind, e.deps.Project, purpose, does),
		Document: strings.Join(roles, "\n"),
	}, nil
}
