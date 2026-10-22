package alb

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	loadBalancerRole = "roles/compute.loadBalancerAdmin"
	certificatesRole = "roles/certificatemanager.owner"
	extensionsRole   = "roles/networkservices.admin"
)

var grants = gcloud.Grants{
	{Role: loadBalancerRole, Permissions: []string{
		"compute.globalAddresses.create",
		"compute.globalAddresses.delete",
		"compute.backendServices.create",
		"compute.backendServices.delete",
		"compute.backendServices.update",
		"compute.backendBuckets.create",
		"compute.backendBuckets.delete",
		"compute.backendBuckets.update",
		"compute.backendBuckets.use",
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
		"compute.projects.get",
	}},
	{Role: certificatesRole, Permissions: []string{
		"certificatemanager.certmaps.create",
		"certificatemanager.certmaps.delete",
		"certificatemanager.certmaps.get",
		"certificatemanager.certmapentries.create",
		"certificatemanager.certmapentries.delete",
		"certificatemanager.certmapentries.list",
		"certificatemanager.operations.get",
	}},
	{Role: extensionsRole, Permissions: []string{
		"networkservices.wasmPlugins.create",
		"networkservices.wasmPlugins.delete",
		"networkservices.wasmPlugins.get",
		"networkservices.wasmPlugins.update",
		"networkservices.wasmPlugins.use",
		"networkservices.lbEdgeExtensions.create",
		"networkservices.lbEdgeExtensions.delete",
		"networkservices.lbEdgeExtensions.get",
		"networkservices.lbEdgeExtensions.update",
		"networkservices.operations.get",
	}},
}

func (e *Edge) credentialPermissions(purpose edge.CredentialPurpose) (edge.CredentialDocument, error) {
	var does string
	switch purpose {
	case edge.PurposeBootstrap:
		does = "provisions the load balancer and takes it down"
	case edge.PurposeDeploy:
		does = "adds the load balancer with the first deploy behind it and takes it down with the last, " +
			"and binds a hostname: a certificate and its map entry, a backend onto a serverless NEG, and a host rule in the url map"
	default:
		return edge.CredentialDocument{}, refusal.Refuse(refusal.CodeInvalid,
			"the %s edge lists its roles for bootstrap or deploy credentials, not %q", Kind, string(purpose))
	}
	return edge.CredentialDocument{
		Heading:  fmt.Sprintf("the roles the %s edge adds on project %s, since a %s credential %s", Kind, e.deps.Project, purpose, does),
		Document: strings.Join(grants.Roles(), "\n"),
	}, nil
}
