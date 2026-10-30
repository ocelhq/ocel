package connector

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

const (
	connectorRunRole     = "roles/run.developer"
	connectorRunAsRole   = "roles/iam.serviceAccountUser"
	connectorObjectsRole = "roles/storage.objectAdmin"
	connectorSecretsRole = "roles/secretmanager.admin"
	connectorLogsRole    = "roles/logging.viewAccessor"
)

var connectorTiers = []environment.Tier{environment.TierProduction, environment.TierPreview}

type projectGrant struct {
	role      string
	condition *policy.Condition
}

func projectGrantsFor(c *gcloud.Clients, allowed []scopev1.Scope) []projectGrant {
	grants := []projectGrant{
		{role: c.ConnectorRolePath()},
		{role: connectorRecordsRole, condition: variables.RecordsCondition(c, connectorTiers...)},
	}
	if slices.Contains(allowed, scopev1.Scope_SCOPE_APPS_READ) {
		grants = append(grants, projectGrant{role: connectorLogsRole, condition: logViewsCondition(c.Names)})
	}
	if slices.Contains(allowed, scopev1.Scope_SCOPE_APPS_WRITE) {
		tiers := tiersCondition(c.Names)
		grants = append(grants,
			projectGrant{role: c.ConnectorRoutingRolePath()},
			projectGrant{role: connectorRunRole, condition: tiers},
			projectGrant{role: connectorRunAsRole, condition: tiers},
			projectGrant{role: connectorObjectsRole, condition: tiers},
			projectGrant{role: connectorSecretsRole, condition: tiers},
		)
	}
	return grants
}

func allProjectGrants(c *gcloud.Clients) []projectGrant {
	return projectGrantsFor(c, []scopev1.Scope{scopev1.Scope_SCOPE_APPS_READ, scopev1.Scope_SCOPE_APPS_WRITE})
}

func tiersCondition(n names.Names) *policy.Condition {
	clauses := []string{fmt.Sprintf("!resource.hasTagKey(%q)", n.TierTagKeyPath())}
	for _, tier := range connectorTiers {
		clauses = append(clauses, fmt.Sprintf("resource.matchTag(%q, %q)", n.TierTagKeyPath(), tier))
	}
	return &policy.Condition{Title: "ocel " + string(n.Namespace()) + " tiers or untagged", Expression: strings.Join(clauses, " || ")}
}

func logViewsCondition(n names.Names) *policy.Condition {
	var clauses []string
	for _, tier := range connectorTiers {
		clauses = append(clauses, fmt.Sprintf("resource.name == %q", n.LogViewPath(tier)))
	}
	return &policy.Condition{Title: "ocel " + string(n.Namespace()) + " logs", Expression: strings.Join(clauses, " || ")}
}

func (p Connector) bindConnectorProject(ctx context.Context, allowed []scopev1.Scope, granting bool) error {
	clients, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	if granting {
		roles := bootstrap.Engine{Clients: clients}
		if err := roles.MakeRole(ctx, bootstrap.NewConnectorRole(clients.Names)); err != nil {
			return err
		}
		if err := roles.MakeRole(ctx, bootstrap.NewConnectorRoutingRole(clients.Names)); err != nil {
			return err
		}
	}
	member := "serviceAccount:" + clients.ConnectorAccountEmail()
	wanted := projectGrantsFor(clients, allowed)
	var changes []policy.Change
	for _, grant := range allProjectGrants(clients) {
		held := granting && slices.ContainsFunc(wanted, func(want projectGrant) bool { return want.role == grant.role })
		changes = append(changes, gcloud.GrantedOrRevoked(grant.role, member, grant.condition, held))
	}
	if _, err := policy.Edit(ctx, gcloud.ProjectPolicy(clients), policy.Each(changes...)); err != nil {
		return fmt.Errorf("grant %s the roles the calls it serves need: %w", member, err)
	}
	return nil
}
