package gcloud

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

const bucketResourcePrefix = "projects/_/buckets/"

func ObjectKindsCondition(title string, buckets naming.Buckets, scope naming.StorageScope, kinds ...naming.ObjectKind) (*policy.Condition, error) {
	starts := make([]string, len(kinds))
	for i, kind := range kinds {
		prefix, err := scope.Prefix(kind)
		if err != nil {
			return nil, err
		}
		objects := bucketResourcePrefix + buckets.Holding(kind) + "/objects/" + prefix
		starts[i] = fmt.Sprintf("resource.name.startsWith(%q)", objects)
	}
	return &policy.Condition{Title: title, Expression: strings.Join(starts, " || ")}, nil
}

func BucketCondition(title, bucket string) *policy.Condition {
	name := bucketResourcePrefix + bucket
	return &policy.Condition{
		Title:      title,
		Expression: fmt.Sprintf("resource.name == %q || resource.name.startsWith(%q)", name, name+"/objects/"),
	}
}

func ListedKindCondition(title, bucket string, kind naming.ObjectKind) *policy.Condition {
	name := bucketResourcePrefix + bucket
	return &policy.Condition{
		Title:      title,
		Expression: fmt.Sprintf("resource.name == %q || resource.name.startsWith(%q)", name, name+"/objects/"+string(kind)+"/"),
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
