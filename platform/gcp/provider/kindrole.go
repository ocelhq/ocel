package gcp

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

var roleKind = kind{
	noun:          "custom role",
	terraform:     tfProjectCustomRole,
	emulatorSkips: true,
	survey: func(ctx context.Context, b bootstrap, _ environment.Tier, target item) (presence, error) {
		role, ok := findCustomRole(b.clients.Names, target.Name)
		if !ok {
			return presence{}, fmt.Errorf("no custom role is named %s", target.Name)
		}
		return b.rolePresence(ctx, role)
	},
	make: func(ctx context.Context, b bootstrap, read survey, target item) error {
		role, ok := findCustomRole(read.Names, target.Name)
		if !ok {
			return fmt.Errorf("no custom role is named %s", target.Name)
		}
		return b.makeRole(ctx, role)
	},
	removal: func(_ survey, target item) removal { return kept(target, reasonRoleKept) },
}

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

type customRole struct {
	id          string
	title       string
	description string
	permissions []string
	driftReason string
}

func newAppAccountsRole(names names.Names) customRole {
	return customRole{
		id:          names.AppAccountsRole(),
		title:       "ocel app accounts (" + string(names.Namespace()) + ")",
		description: "lets a deploy create the service accounts its apps run as and set who may act as them, and nothing else",
		permissions: appAccountsPermissions,
		driftReason: reasonRoleDrifted,
	}
}

func findCustomRole(names names.Names, id string) (customRole, bool) {
	for _, role := range []customRole{newAppAccountsRole(names), newCDNPurgeRole(names), newPartsRole(names), newPartDatabasesRole(names), newProjectRecordsRole(names)} {
		if role.id == id {
			return role, true
		}
	}
	return customRole{}, false
}

func (b bootstrap) rolePresence(ctx context.Context, want customRole) (presence, error) {
	id := want.id
	role, err := b.readRole(ctx, id)
	if gcloud.Absent(err) {
		return presence{}, nil
	}
	if err != nil {
		return presence{}, fmt.Errorf("read the %s custom role: %w", id, err)
	}
	switch {
	case role.Deleted:
		return presence{present: true, mends: reasonRoleDeleted}, nil
	case roleDrifted(role, want):
		return presence{present: true, mends: want.driftReason}, nil
	}
	return presence{present: true}, nil
}

func (b bootstrap) readRole(ctx context.Context, id string) (*iam.Role, error) {
	service, err := b.clients.Accounts()
	if err != nil {
		return nil, err
	}
	return googleretry.Attempted(ctx, service.Projects.Roles.Get(roleName(b.clients, id)).Context(ctx).Do)
}

func roleName(c *gcloud.Clients, id string) string { return "projects/" + c.Project() + "/roles/" + id }

func roleDrifted(role *iam.Role, want customRole) bool {
	return role.Stage == roleStageDisabled ||
		!slices.Equal(slices.Sorted(slices.Values(role.IncludedPermissions)), slices.Sorted(slices.Values(want.permissions)))
}

func (b bootstrap) makeRole(ctx context.Context, want customRole) error {
	id := want.id
	service, err := b.clients.Accounts()
	if err != nil {
		return err
	}
	role, err := b.readRole(ctx, id)
	if gcloud.Absent(err) {
		role, err = b.createRole(ctx, want)
	}
	if err != nil {
		return err
	}
	name := roleName(b.clients, id)
	if role.Deleted {
		if role, err = googleretry.Attempted(ctx, service.Projects.Roles.Undelete(name, &iam.UndeleteRoleRequest{Etag: role.Etag}).Context(ctx).Do); err != nil {
			if googleretry.AnsweredCode(err) == http.StatusBadRequest || gcloud.Absent(err) {
				return refusePurgedRole(name)
			}
			return fmt.Errorf("undelete the %s custom role: %w", id, err)
		}
	}
	if !roleDrifted(role, want) {
		return nil
	}
	if _, err := googleretry.Attempted(ctx, service.Projects.Roles.Patch(name, &iam.Role{
		IncludedPermissions: want.permissions,
		Stage:               roleStageGA,
		Etag:                role.Etag,
	}).UpdateMask("includedPermissions,stage").Context(ctx).Do); err != nil {
		return fmt.Errorf("put the %s custom role back to its permissions: %w", id, err)
	}
	return nil
}

func (b bootstrap) createRole(ctx context.Context, want customRole) (*iam.Role, error) {
	id := want.id
	service, err := b.clients.Accounts()
	if err != nil {
		return nil, err
	}
	created, err := googleretry.Attempted(ctx, service.Projects.Roles.Create("projects/"+b.clients.Project(), &iam.CreateRoleRequest{
		RoleId: id,
		Role: &iam.Role{
			Title:               want.title,
			Description:         want.description,
			IncludedPermissions: want.permissions,
			Stage:               roleStageGA,
		},
	}).Context(ctx).Do)
	if gcloud.Taken(err) {
		held, readErr := b.readRole(ctx, id)
		if gcloud.Absent(readErr) {
			return nil, refusePurgedRole(roleName(b.clients, id))
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
