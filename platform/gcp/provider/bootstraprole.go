package gcp

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	reasonRoleDeleted = "it was deleted, and its bindings grant nothing until it is undeleted"
	reasonRoleDrifted = "it holds other permissions than a deploy needs on the service accounts its apps run as"
	reasonRoleKept    = "Google holds a deleted custom role's id for up to 44 days, so this role outlives every bootstrap that used it"

	roleStageGA       = "GA"
	roleStageDisabled = "DISABLED"
)

var appAccountsPermissions = []string{
	"iam.serviceAccounts.create",
	"iam.serviceAccounts.get",
	"iam.serviceAccounts.getIamPolicy",
	"iam.serviceAccounts.setIamPolicy",
}

func (b bootstrap) rolePresence(ctx context.Context, id string) (presence, error) {
	role, err := b.readRole(ctx, id)
	if absent(err) {
		return presence{}, nil
	}
	if err != nil {
		return presence{}, fmt.Errorf("read the %s custom role: %w", id, err)
	}
	switch {
	case role.Deleted:
		return presence{present: true, mends: reasonRoleDeleted}, nil
	case roleDrifted(role):
		return presence{present: true, mends: reasonRoleDrifted}, nil
	}
	return presence{present: true}, nil
}

func (b bootstrap) readRole(ctx context.Context, id string) (*iam.Role, error) {
	service, err := b.clients.Accounts()
	if err != nil {
		return nil, err
	}
	return attempted(ctx, service.Projects.Roles.Get(b.clients.roleName(id)).Context(ctx).Do)
}

func (c *clients) roleName(id string) string { return "projects/" + c.project + "/roles/" + id }

func roleDrifted(role *iam.Role) bool {
	return role.Stage == roleStageDisabled ||
		!slices.Equal(slices.Sorted(slices.Values(role.IncludedPermissions)), slices.Sorted(slices.Values(appAccountsPermissions)))
}

func (b bootstrap) makeRole(ctx context.Context, id string) error {
	service, err := b.clients.Accounts()
	if err != nil {
		return err
	}
	role, err := b.readRole(ctx, id)
	if absent(err) {
		role, err = b.createRole(ctx, id)
	}
	if err != nil {
		return err
	}
	name := b.clients.roleName(id)
	if role.Deleted {
		if role, err = attempted(ctx, service.Projects.Roles.Undelete(name, &iam.UndeleteRoleRequest{Etag: role.Etag}).Context(ctx).Do); err != nil {
			if answeredCode(err) == http.StatusBadRequest || absent(err) {
				return refusePurgedRole(name)
			}
			return fmt.Errorf("undelete the %s custom role: %w", id, err)
		}
	}
	if !roleDrifted(role) {
		return nil
	}
	if _, err := attempted(ctx, service.Projects.Roles.Patch(name, &iam.Role{
		IncludedPermissions: appAccountsPermissions,
		Stage:               roleStageGA,
		Etag:                role.Etag,
	}).UpdateMask("includedPermissions,stage").Context(ctx).Do); err != nil {
		return fmt.Errorf("put the %s custom role back to the permissions a deploy needs: %w", id, err)
	}
	return nil
}

func (b bootstrap) createRole(ctx context.Context, id string) (*iam.Role, error) {
	service, err := b.clients.Accounts()
	if err != nil {
		return nil, err
	}
	namespace := b.clients.Namespace()
	created, err := attempted(ctx, service.Projects.Roles.Create("projects/"+b.clients.project, &iam.CreateRoleRequest{
		RoleId: id,
		Role: &iam.Role{
			Title:               "ocel app accounts (" + string(namespace) + ")",
			Description:         "lets a deploy create the service accounts its apps run as and set who may act as them, and nothing else",
			IncludedPermissions: appAccountsPermissions,
			Stage:               roleStageGA,
		},
	}).Context(ctx).Do)
	if taken(err) {
		held, readErr := b.readRole(ctx, id)
		if absent(readErr) {
			return nil, refusePurgedRole(b.clients.roleName(id))
		}
		if readErr != nil {
			return nil, fmt.Errorf("read the %s custom role that already exists: %w", id, readErr)
		}
		return held, nil
	}
	if err != nil {
		return nil, fmt.Errorf("create the %s custom role: %w", id, err)
	}
	return created, nil
}

func refusePurgedRole(path string) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"the custom role %s was deleted, and Google undeletes a role only in the first 7 days and holds its id from reuse until it is purged, up to 44 days after the deletion.\n"+
			"Bootstrap again once Google has purged it, or under another namespace in %s",
		path, provider.NamespaceEnvVar)
}
