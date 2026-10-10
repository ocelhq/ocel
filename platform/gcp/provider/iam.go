package gcp

import (
	"context"
	"fmt"
	"slices"

	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iam/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	conditionalPolicyVersion = 3

	bindAttempts = 4
)

func databaseCondition(project string, ns provider.Namespace) *cloudresourcemanager.Expr {
	return &cloudresourcemanager.Expr{
		Title: "ocel " + string(ns) + " database",
		Expression: fmt.Sprintf("resource.name == %q",
			fmt.Sprintf("projects/%s/databases/%s", project, ports.Database(ns))),
	}
}

func (c *clients) objectPrefixCondition(title string, tier environment.Tier, prefix string) *cloudresourcemanager.Expr {
	return &cloudresourcemanager.Expr{
		Title:      title,
		Expression: fmt.Sprintf("resource.name.startsWith(%q)", "projects/_/buckets/"+c.Bucket(tier)+"/objects/"+prefix),
	}
}

func sameCondition(current, want *cloudresourcemanager.Expr) bool {
	if current == nil || want == nil {
		return current == nil && want == nil
	}
	return current.Expression == want.Expression
}

func (c *clients) projectPolicy(ctx context.Context) (*cloudresourcemanager.Policy, error) {
	service, err := c.Projects()
	if err != nil {
		return nil, err
	}
	policy, err := attempted(ctx, service.Projects.GetIamPolicy(c.project,
		&cloudresourcemanager.GetIamPolicyRequest{
			Options: &cloudresourcemanager.GetPolicyOptions{RequestedPolicyVersion: conditionalPolicyVersion},
		}).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("read who may reach project %s's records: %w", c.project, err)
	}
	return policy, nil
}

func (c *clients) projectRoleGranted(ctx context.Context, member, role string, condition *cloudresourcemanager.Expr) (bool, error) {
	policy, err := c.projectPolicy(ctx)
	if err != nil {
		return false, err
	}
	for _, binding := range policy.Bindings {
		if binding.Role == role && sameCondition(binding.Condition, condition) && slices.Contains(binding.Members, member) {
			return true, nil
		}
	}
	return false, nil
}

func (c *clients) bindProjectRole(ctx context.Context, member, role string, condition *cloudresourcemanager.Expr, granting bool) error {
	service, err := c.Projects()
	if err != nil {
		return err
	}
	var refused error
	for attempt := range bindAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return ctx.Err()
		}
		policy, err := c.projectPolicy(ctx)
		if err != nil {
			return err
		}
		bindings, changed := boundMember(policy.Bindings, role, member, condition, granting)
		if !changed {
			return nil
		}
		policy.Bindings = bindings
		policy.Version = conditionalPolicyVersion
		_, refused = attempted(ctx, service.Projects.SetIamPolicy(c.project,
			&cloudresourcemanager.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do)
		if refused == nil {
			return nil
		}
		if !stale(refused) {
			break
		}
	}
	return fmt.Errorf("update the %s binding of %s on project %s: %w", role, member, c.project, refused)
}

func (c *clients) keyPolicy(ctx context.Context, tier environment.Tier) (*iampb.Policy, error) {
	client, err := c.KMS()
	if err != nil {
		return nil, err
	}
	key := keyPath(c, string(tier))
	if _, err := dialled(ctx, func() (*kmspb.CryptoKey, error) {
		return client.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: key})
	}); err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("read the %s key: %w", tier, err)
	}
	policy, err := dialled(ctx, func() (*iampb.Policy, error) {
		return client.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: key})
	})
	if err != nil {
		return nil, fmt.Errorf("read who may use the %s key: %w", tier, err)
	}
	return policy, nil
}

func (c *clients) keyRolesGranted(ctx context.Context, tier environment.Tier, member string, roles []string) (bool, error) {
	policy, err := c.keyPolicy(ctx, tier)
	if err != nil || policy == nil {
		return false, err
	}
	for _, role := range roles {
		if !slices.ContainsFunc(policy.GetBindings(), func(binding *iampb.Binding) bool {
			return binding.GetRole() == role && slices.Contains(binding.GetMembers(), member)
		}) {
			return false, nil
		}
	}
	return true, nil
}

func (c *clients) bindKeyRoles(ctx context.Context, tier environment.Tier, member string, roles, wanted []string) (bool, error) {
	client, err := c.KMS()
	if err != nil {
		return false, err
	}
	var refused error
	for attempt := range bindAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return false, ctx.Err()
		}
		policy, err := c.keyPolicy(ctx, tier)
		if err != nil || policy == nil {
			return false, err
		}
		bindings, changed := boundKeyRoles(policy.GetBindings(), member, roles, wanted)
		if !changed {
			return false, nil
		}
		policy.Bindings = bindings
		_, refused = dialled(ctx, func() (*iampb.Policy, error) {
			return client.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{Resource: keyPath(c, string(tier)), Policy: policy})
		})
		if refused == nil {
			return true, nil
		}
		if !stale(refused) {
			break
		}
	}
	return false, fmt.Errorf("set the roles %s has on the %s key to %v: %w", member, tier, wanted, refused)
}

func boundMember(bindings []*cloudresourcemanager.Binding, role, member string,
	condition *cloudresourcemanager.Expr, granting bool) ([]*cloudresourcemanager.Binding, bool) {
	for i, binding := range bindings {
		if binding.Role != role || !sameCondition(binding.Condition, condition) {
			continue
		}
		members, changed := boundMembers(binding.Members, member, granting)
		if !changed {
			return bindings, false
		}
		bound := slices.Clone(bindings)
		narrowed := *binding
		narrowed.Members = members
		bound[i] = &narrowed
		return bound, true
	}
	if !granting {
		return bindings, false
	}
	return append(bindings, &cloudresourcemanager.Binding{
		Role:      role,
		Members:   []string{member},
		Condition: condition,
	}), true
}

func boundKeyRoles(bindings []*iampb.Binding, member string, roles, wanted []string) ([]*iampb.Binding, bool) {
	changed := false
	for _, role := range roles {
		var roleChanged bool
		bindings, roleChanged = boundKeyMember(bindings, role, member, slices.Contains(wanted, role))
		changed = changed || roleChanged
	}
	return bindings, changed
}

func boundKeyMember(bindings []*iampb.Binding, role, member string, granting bool) ([]*iampb.Binding, bool) {
	for i, binding := range bindings {
		if binding.GetRole() != role {
			continue
		}
		members, changed := boundMembers(binding.GetMembers(), member, granting)
		if !changed {
			return bindings, false
		}
		bound := slices.Clone(bindings)
		replaced := proto.Clone(binding).(*iampb.Binding)
		replaced.Members = members
		bound[i] = replaced
		return bound, true
	}
	if !granting {
		return bindings, false
	}
	return append(bindings, &iampb.Binding{Role: role, Members: []string{member}}), true
}

func (c *clients) accountRoleGranted(ctx context.Context, account, role, member string) (bool, error) {
	service, err := c.Accounts()
	if err != nil {
		return false, err
	}
	policy, err := attempted(ctx, service.Projects.ServiceAccounts.GetIamPolicy(accountPath(c, account)).Context(ctx).Do)
	if absent(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read who may act as the %s service account: %w", account, err)
	}
	return slices.ContainsFunc(policy.Bindings, func(binding *iam.Binding) bool {
		return binding.Role == role && binding.Condition == nil && slices.Contains(binding.Members, member)
	}), nil
}

func (c *clients) bindAccountRole(ctx context.Context, account, role, member string, granting bool) error {
	service, err := c.Accounts()
	if err != nil {
		return err
	}
	var refused error
	for attempt := range bindAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return ctx.Err()
		}
		policy, err := attempted(ctx, service.Projects.ServiceAccounts.GetIamPolicy(accountPath(c, account)).Context(ctx).Do)
		if absent(err) && !granting {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read who may act as the %s service account: %w", account, err)
		}
		bindings, changed := boundAccountMember(policy.Bindings, role, member, granting)
		if !changed {
			return nil
		}
		policy.Bindings = bindings
		_, refused = attempted(ctx, service.Projects.ServiceAccounts.SetIamPolicy(accountPath(c, account),
			&iam.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do)
		if refused == nil || !stale(refused) {
			break
		}
	}
	if refused != nil {
		return fmt.Errorf("update the %s binding of %s on the %s service account: %w", role, member, account, refused)
	}
	return nil
}

func (c *clients) unbindAccountRole(ctx context.Context, account, role string) ([]string, error) {
	service, err := c.Accounts()
	if err != nil {
		return nil, err
	}
	var refused error
	for attempt := range bindAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return nil, ctx.Err()
		}
		policy, err := attempted(ctx, service.Projects.ServiceAccounts.GetIamPolicy(accountPath(c, account)).Context(ctx).Do)
		if absent(err) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read who may act as the %s service account: %w", account, err)
		}
		var removed []string
		policy.Bindings = slices.DeleteFunc(policy.Bindings, func(binding *iam.Binding) bool {
			if binding.Role != role || binding.Condition != nil {
				return false
			}
			removed = append(removed, binding.Members...)
			return true
		})
		if len(removed) == 0 {
			return nil, nil
		}
		_, refused = attempted(ctx, service.Projects.ServiceAccounts.SetIamPolicy(accountPath(c, account),
			&iam.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do)
		if refused == nil {
			return removed, nil
		}
		if !stale(refused) {
			break
		}
	}
	return nil, fmt.Errorf("remove the %s binding from the %s service account: %w", role, account, refused)
}

func boundAccountMember(bindings []*iam.Binding, role, member string, granting bool) ([]*iam.Binding, bool) {
	for i, binding := range bindings {
		if binding.Role != role || binding.Condition != nil {
			continue
		}
		members, changed := boundMembers(binding.Members, member, granting)
		if !changed {
			return bindings, false
		}
		bound := slices.Clone(bindings)
		narrowed := *binding
		narrowed.Members = members
		bound[i] = &narrowed
		return bound, true
	}
	if !granting {
		return bindings, false
	}
	return append(bindings, &iam.Binding{Role: role, Members: []string{member}}), true
}

func removeMemberFromRoles(bindings []*cloudresourcemanager.Binding, member string, roles []string) (kept, removed []*cloudresourcemanager.Binding) {
	kept = make([]*cloudresourcemanager.Binding, 0, len(bindings))
	for _, binding := range bindings {
		if slices.Contains(roles, binding.Role) && slices.Contains(binding.Members, member) {
			removed = append(removed, &cloudresourcemanager.Binding{Role: binding.Role, Condition: binding.Condition, Members: []string{member}})
			narrowed := *binding
			narrowed.Members = slices.DeleteFunc(slices.Clone(binding.Members), func(held string) bool { return held == member })
			if len(narrowed.Members) == 0 {
				continue
			}
			binding = &narrowed
		}
		kept = append(kept, binding)
	}
	return kept, removed
}

func (c *clients) unbindProjectMember(ctx context.Context, member string) ([]*cloudresourcemanager.Binding, error) {
	service, err := c.Projects()
	if err != nil {
		return nil, err
	}
	var refused error
	for attempt := range bindAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return nil, ctx.Err()
		}
		policy, err := c.projectPolicy(ctx)
		if err != nil {
			return nil, err
		}
		bindings, removed := removeMemberFromRoles(policy.Bindings, member, listGrantedRoles(c.Names))
		if len(removed) == 0 {
			return nil, nil
		}
		policy.Bindings = bindings
		policy.Version = conditionalPolicyVersion
		_, refused = attempted(ctx, service.Projects.SetIamPolicy(c.project,
			&cloudresourcemanager.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do)
		if refused == nil {
			return removed, nil
		}
		if !stale(refused) {
			break
		}
	}
	return nil, fmt.Errorf("revoke the roles ocel granted %s on project %s: %w", member, c.project, refused)
}
