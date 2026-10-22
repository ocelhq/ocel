package gcloud

import "slices"

type Grant struct {
	Role        string
	Permissions []string
}

type Grants []Grant

func (g Grants) Permissions() []string {
	var permissions []string
	for _, grant := range g {
		for _, permission := range grant.Permissions {
			if !slices.Contains(permissions, permission) {
				permissions = append(permissions, permission)
			}
		}
	}
	return permissions
}

func (g Grants) Roles() []string {
	var roles []string
	for _, grant := range g {
		if !slices.Contains(roles, grant.Role) {
			roles = append(roles, grant.Role)
		}
	}
	return roles
}

func (g Grants) RolesGranting(permissions []string) []string {
	var roles []string
	for _, permission := range permissions {
		at := slices.IndexFunc(g, func(grant Grant) bool { return slices.Contains(grant.Permissions, permission) })
		if at >= 0 && !slices.Contains(roles, g[at].Role) {
			roles = append(roles, g[at].Role)
		}
	}
	return roles
}
