package connector

import (
	"slices"
	"testing"

	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/scope"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func grantedClients() *gcloud.Clients {
	return &gcloud.Clients{Names: names.New(provider.Namespace("ocel"), "acme-prod"), Region: "europe-west1"}
}

func rolesOf(grants []projectGrant) []string {
	roles := make([]string, 0, len(grants))
	for _, grant := range grants {
		roles = append(roles, grant.role)
	}
	return roles
}

func TestAConnectorWithEveryGrantHoldsARoleForEveryCallClassItsControlAssemblyServes(t *testing.T) {
	t.Parallel()

	c := grantedClients()
	held := rolesOf(projectGrantsFor(c, scope.ListUserScopes()))
	base := bootstrap.NewConnectorRole(c.Names)
	routing := bootstrap.NewConnectorRoutingRole(c.Names)
	for class, need := range map[string]struct {
		role       string
		permission string
	}{
		"reading and writing records, stack records and tag records": {role: connectorRecordsRole},
		"checking the project every call opens its clients in":       {role: c.ConnectorRolePath(), permission: "resourcemanager.projects.get"},
		"checking the region every call opens its clients in":        {role: c.ConnectorRolePath(), permission: "run.locations.list"},
		"checking the project's billing every call opens its clients in": {
			role: c.ConnectorRolePath(), permission: "serviceusage.services.get",
		},
		"routing a hostname through the alb's url map":      {role: c.ConnectorRoutingRolePath(), permission: "compute.urlMaps.update"},
		"pointing a host rule at a backend service":         {role: c.ConnectorRoutingRolePath(), permission: "compute.backendServices.use"},
		"pointing a path rule at the static files' bucket":  {role: c.ConnectorRoutingRolePath(), permission: "compute.backendBuckets.use"},
		"waiting on a url map write":                        {role: c.ConnectorRoutingRolePath(), permission: "compute.globalOperations.get"},
		"purging a release's pages from Cloud CDN":          {role: c.ConnectorRoutingRolePath(), permission: "compute.urlMaps.invalidateCache"},
		"reading the alb's address":                         {role: c.ConnectorRoutingRolePath(), permission: "compute.globalAddresses.get"},
		"reading the backend service quota":                 {role: c.ConnectorRoutingRolePath(), permission: "compute.projects.get"},
		"listing the hostnames a certificate map holds":     {role: c.ConnectorRoutingRolePath(), permission: "certificatemanager.certmapentries.list"},
		"issuing a hostname's certificate":                  {role: c.ConnectorRoutingRolePath(), permission: "certificatemanager.certs.create"},
		"authorizing a hostname's certificate by DNS":       {role: c.ConnectorRoutingRolePath(), permission: "certificatemanager.dnsauthorizations.create"},
		"moving a promotion's Cloud Run traffic":            {role: connectorRunRole},
		"updating a service that runs as its app's account": {role: connectorRunAsRole},
		"copying and removing a release's static files":     {role: connectorObjectsRole},
		"publishing a protected project's gate secrets":     {role: connectorSecretsRole},
		"reading and tailing an app's logs":                 {role: connectorLogsRole},
	} {
		if !slices.Contains(held, need.role) {
			t.Errorf("%s needs %s, and the connector holds %v", class, need.role, held)
			continue
		}
		for _, custom := range []struct {
			path        string
			permissions []string
		}{{c.ConnectorRolePath(), base.Permissions}, {c.ConnectorRoutingRolePath(), routing.Permissions}} {
			if need.permission != "" && need.role == custom.path && !slices.Contains(custom.permissions, need.permission) {
				t.Errorf("%s needs %s, and %s holds only %v", class, need.permission, custom.path, custom.permissions)
			}
		}
	}
}

func TestAConnectorHoldsOnlyTheProjectRolesItsGrantsCallFor(t *testing.T) {
	t.Parallel()

	c := grantedClients()
	always := []string{c.ConnectorRolePath(), connectorRecordsRole}
	for name, tc := range map[string]struct {
		allowed []scopev1.Scope
		roles   []string
	}{
		"variables alone": {allowed: []scopev1.Scope{scopev1.Scope_SCOPE_VARIABLES_READ}, roles: always},
		"apps read":       {allowed: []scopev1.Scope{scopev1.Scope_SCOPE_APPS_READ}, roles: append(slices.Clone(always), connectorLogsRole)},
		"apps write": {allowed: []scopev1.Scope{scopev1.Scope_SCOPE_APPS_WRITE}, roles: append(slices.Clone(always),
			c.ConnectorRoutingRolePath(), connectorRunRole, connectorRunAsRole, connectorObjectsRole, connectorSecretsRole)},
	} {
		if got := rolesOf(projectGrantsFor(c, tc.allowed)); !slices.Equal(got, tc.roles) {
			t.Errorf("%s: the connector holds %v, want %v: an app read never routes, and a variable never reads a log", name, got, tc.roles)
		}
	}
}

func TestEveryTieredConnectorRoleIsHeldOnlyOverItsNamespacesTiersOrUntaggedResources(t *testing.T) {
	t.Parallel()

	c := grantedClients()
	for _, grant := range projectGrantsFor(c, scope.ListUserScopes()) {
		switch grant.role {
		case connectorRunRole, connectorRunAsRole, connectorObjectsRole, connectorSecretsRole:
			if grant.condition == nil || grant.condition.Expression != tiersCondition(c.Names).Expression {
				t.Errorf("%s is held on %v, want only the tiers' or untagged resources: another tag's resources are no business of this connector", grant.role, grant.condition)
			}
		}
	}
}
