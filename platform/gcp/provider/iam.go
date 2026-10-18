package gcp

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/iampolicy"
)

func (c *clients) objectPrefixCondition(title string, tier environment.Tier, prefix string) *iampolicy.Condition {
	return &iampolicy.Condition{
		Title:      title,
		Expression: fmt.Sprintf("resource.name.startsWith(%q)", "projects/_/buckets/"+c.Bucket(tier)+"/objects/"+prefix),
	}
}

func grantedOrRevoked(role, member string, condition *iampolicy.Condition, granting bool) iampolicy.Change {
	if granting {
		return iampolicy.Grant(role, member, condition)
	}
	return iampolicy.Revoke(role, member, condition)
}

func (c *clients) projectRoleGranted(ctx context.Context, member, role string, condition *iampolicy.Condition) (bool, error) {
	bindings, err := iampolicy.Read(ctx, c.projectPolicy())
	if err != nil {
		return false, err
	}
	return iampolicy.Holds(bindings, role, member, condition), nil
}

func (c *clients) bindProjectRole(ctx context.Context, member, role string, condition *iampolicy.Condition, granting bool) error {
	_, err := iampolicy.Edit(ctx, c.projectPolicy(), grantedOrRevoked(role, member, condition, granting))
	return err
}

func (c *clients) tierKeyPolicy(tier environment.Tier) iampolicy.Resource {
	return c.keyPolicy(keyPath(c, string(tier)), string(tier))
}

func (c *clients) keyRolesGranted(ctx context.Context, tier environment.Tier, member string, roles []string) (bool, error) {
	bindings, err := iampolicy.Read(ctx, c.tierKeyPolicy(tier))
	if iampolicy.IsAbsent(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, role := range roles {
		if !iampolicy.Holds(bindings, role, member, nil) {
			return false, nil
		}
	}
	return true, nil
}

func (c *clients) bindKeyRoles(ctx context.Context, tier environment.Tier, member string, roles, wanted []string) (bool, error) {
	changes := make([]iampolicy.Change, 0, len(roles))
	for _, role := range roles {
		changes = append(changes, grantedOrRevoked(role, member, nil, slices.Contains(wanted, role)))
	}
	changed, err := iampolicy.Edit(ctx, c.tierKeyPolicy(tier), iampolicy.Each(changes...))
	if iampolicy.IsAbsent(err) {
		return false, nil
	}
	return changed, err
}

func (c *clients) accountRoleGranted(ctx context.Context, account, role, member string) (bool, error) {
	bindings, err := iampolicy.Read(ctx, c.accountPolicy(account))
	if iampolicy.IsAbsent(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return iampolicy.Holds(bindings, role, member, nil), nil
}

func (c *clients) bindAccountRole(ctx context.Context, account, role, member string, granting bool) error {
	_, err := iampolicy.Edit(ctx, c.accountPolicy(account), grantedOrRevoked(role, member, nil, granting))
	if iampolicy.IsAbsent(err) && !granting {
		return nil
	}
	return err
}

func (c *clients) unbindProjectMember(ctx context.Context, member string) ([]iampolicy.Binding, error) {
	roles := listGrantedRoles(c.Names)
	var removed []iampolicy.Binding
	_, err := iampolicy.Edit(ctx, c.projectPolicy(), func(bindings []iampolicy.Binding) ([]iampolicy.Binding, bool) {
		removed = nil
		for _, binding := range bindings {
			if slices.Contains(roles, binding.Role) && slices.Contains(binding.Members, member) {
				removed = append(removed, iampolicy.Binding{Role: binding.Role, Condition: binding.Condition, Members: []string{member}})
			}
		}
		return iampolicy.RevokeRoles(member, roles)(bindings)
	})
	if err != nil {
		return nil, fmt.Errorf("revoke the roles ocel granted %s on project %s: %w", member, c.project, err)
	}
	return removed, nil
}
