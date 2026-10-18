package gcloud

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

func ObjectPrefixCondition(c *Clients, title string, tier environment.Tier, prefix string) *policy.Condition {
	return &policy.Condition{
		Title:      title,
		Expression: fmt.Sprintf("resource.name.startsWith(%q)", "projects/_/buckets/"+c.Bucket(tier)+"/objects/"+prefix),
	}
}

func GrantedOrRevoked(role, member string, condition *policy.Condition, granting bool) policy.Change {
	if granting {
		return policy.Grant(role, member, condition)
	}
	return policy.Revoke(role, member, condition)
}

func ProjectRoleGranted(ctx context.Context, c *Clients, member, role string, condition *policy.Condition) (bool, error) {
	bindings, err := policy.Read(ctx, ProjectPolicy(c))
	if err != nil {
		return false, err
	}
	return policy.Holds(bindings, role, member, condition), nil
}

func BindProjectRole(ctx context.Context, c *Clients, member, role string, condition *policy.Condition, granting bool) error {
	_, err := policy.Edit(ctx, ProjectPolicy(c), GrantedOrRevoked(role, member, condition, granting))
	return err
}

func tierKeyPolicy(c *Clients, tier environment.Tier) policy.Resource {
	return KeyPolicy(c, KeyPath(c, string(tier)), string(tier))
}

func KeyRolesGranted(ctx context.Context, c *Clients, tier environment.Tier, member string, roles []string) (bool, error) {
	bindings, err := policy.Read(ctx, tierKeyPolicy(c, tier))
	if policy.IsAbsent(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, role := range roles {
		if !policy.Holds(bindings, role, member, nil) {
			return false, nil
		}
	}
	return true, nil
}

func BindKeyRoles(ctx context.Context, c *Clients, tier environment.Tier, member string, roles, wanted []string) (bool, error) {
	changes := make([]policy.Change, 0, len(roles))
	for _, role := range roles {
		changes = append(changes, GrantedOrRevoked(role, member, nil, slices.Contains(wanted, role)))
	}
	changed, err := policy.Edit(ctx, tierKeyPolicy(c, tier), policy.Each(changes...))
	if policy.IsAbsent(err) {
		return false, nil
	}
	return changed, err
}

func AccountRoleGranted(ctx context.Context, c *Clients, account, role, member string) (bool, error) {
	bindings, err := policy.Read(ctx, AccountPolicy(c, account))
	if policy.IsAbsent(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return policy.Holds(bindings, role, member, nil), nil
}

func BindAccountRole(ctx context.Context, c *Clients, account, role, member string, granting bool) error {
	_, err := policy.Edit(ctx, AccountPolicy(c, account), GrantedOrRevoked(role, member, nil, granting))
	if policy.IsAbsent(err) && !granting {
		return nil
	}
	return err
}
