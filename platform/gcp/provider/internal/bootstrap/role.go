package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

var roleKind = KindOps{
	noun:          "custom role",
	terraform:     cost.ProjectCustomRoleType,
	emulatorSkips: true,
	survey: func(ctx context.Context, b Engine, _ Survey, target Resource) (Presence, error) {
		role, ok := FindCustomRole(b.Clients.Names, target.Name)
		if !ok {
			return Presence{}, fmt.Errorf("no custom role is named %s", target.Name)
		}
		return b.rolePresence(ctx, role)
	},
	Make: func(ctx context.Context, b Engine, read Survey, target Resource) error {
		role, ok := FindCustomRole(read.Names, target.Name)
		if !ok {
			return fmt.Errorf("no custom role is named %s", target.Name)
		}
		return b.MakeRole(ctx, role)
	},
	removal: func(_ Survey, target Resource) Removal { return kept(target, ReasonRoleKept) },
}

const (
	ReasonRoleDeleted = "it was deleted, and its bindings grant nothing until it is undeleted"
	ReasonRoleKept    = "Google holds a deleted custom role's id for up to 44 days, so this role outlives every bootstrap that used it"

	roleStageGA       = "GA"
	roleStageDisabled = "DISABLED"
)

type customRole struct {
	ID          string
	title       string
	description string
	Permissions []string
	DriftReason string
}

func FindCustomRole(names names.Names, id string) (customRole, bool) {
	for _, role := range []customRole{NewCDNPurgeRole(names), NewPartsRole(names)} {
		if role.ID == id {
			return role, true
		}
	}
	return customRole{}, false
}

func (b Engine) rolePresence(ctx context.Context, want customRole) (Presence, error) {
	id := want.ID
	role, err := b.readRole(ctx, id)
	if gcloud.Absent(err) {
		return Presence{}, nil
	}
	if err != nil {
		return Presence{}, fmt.Errorf("read the %s custom role: %w", id, err)
	}
	switch {
	case role.Deleted:
		return Presence{Present: true, Mends: ReasonRoleDeleted}, nil
	case roleDrifted(role, want):
		return Presence{Present: true, Mends: want.DriftReason}, nil
	}
	return Presence{Present: true}, nil
}

func (b Engine) readRole(ctx context.Context, id string) (*iam.Role, error) {
	service, err := b.Clients.Accounts()
	if err != nil {
		return nil, err
	}
	return googleretry.Attempted(ctx, service.Projects.Roles.Get(roleName(b.Clients, id)).Context(ctx).Do)
}

func roleName(c *gcloud.Clients, id string) string { return "projects/" + c.Project() + "/roles/" + id }

func roleDrifted(role *iam.Role, want customRole) bool {
	return role.Stage == roleStageDisabled ||
		!slices.Equal(slices.Sorted(slices.Values(role.IncludedPermissions)), slices.Sorted(slices.Values(want.Permissions)))
}

func (b Engine) MakeRole(ctx context.Context, want customRole) error {
	id := want.ID
	service, err := b.Clients.Accounts()
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
	name := roleName(b.Clients, id)
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
		IncludedPermissions: want.Permissions,
		Stage:               roleStageGA,
		Etag:                role.Etag,
	}).UpdateMask("includedPermissions,stage").Context(ctx).Do); err != nil {
		return fmt.Errorf("put the %s custom role back to its permissions: %w", id, err)
	}
	return nil
}

func (b Engine) createRole(ctx context.Context, want customRole) (*iam.Role, error) {
	id := want.ID
	service, err := b.Clients.Accounts()
	if err != nil {
		return nil, err
	}
	created, err := googleretry.Attempted(ctx, service.Projects.Roles.Create("projects/"+b.Clients.Project(), &iam.CreateRoleRequest{
		RoleId: id,
		Role: &iam.Role{
			Title:               want.title,
			Description:         want.description,
			IncludedPermissions: want.Permissions,
			Stage:               roleStageGA,
		},
	}).Context(ctx).Do)
	if gcloud.Taken(err) {
		held, readErr := b.readRole(ctx, id)
		if gcloud.Absent(readErr) {
			return nil, refusePurgedRole(roleName(b.Clients, id))
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

func NewCDNPurgeRole(names names.Names) customRole {
	return customRole{
		ID:          names.CDNPurgeRole(),
		title:       "ocel cache purge (" + string(names.Namespace()) + ")",
		description: "lets a Next app behind the alb clear its revalidated pages from Cloud CDN, and nothing else",
		Permissions: []string{"compute.urlMaps.invalidateCache"},
		DriftReason: "it holds other permissions than clearing Cloud CDN, which is all a Next app behind the alb is granted it for",
	}
}
