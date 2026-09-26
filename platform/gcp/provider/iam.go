package gcp

import (
	"context"
	"fmt"
	"slices"

	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

func sameCondition(held, want *cloudresourcemanager.Expr) bool {
	if held == nil || want == nil {
		return held == nil && want == nil
	}
	return held.Expression == want.Expression
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

func (c *clients) projectRoleHeld(ctx context.Context, member, role string, condition *cloudresourcemanager.Expr) (bool, error) {
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
		if !taken(refused) {
			break
		}
	}
	return fmt.Errorf("hold %s to %s on project %s: %w", member, role, c.project, refused)
}

func (c *clients) keyPolicy(ctx context.Context, class edge.Class) (*iampb.Policy, error) {
	client, err := c.KMS()
	if err != nil {
		return nil, err
	}
	key := keyPath(c, string(class))
	if _, err := dialled(ctx, func() (*kmspb.CryptoKey, error) {
		return client.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: key})
	}); err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("read the %s key: %w", class, err)
	}
	policy, err := dialled(ctx, func() (*iampb.Policy, error) {
		return client.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: key})
	})
	if err != nil {
		return nil, fmt.Errorf("read who may use the %s key: %w", class, err)
	}
	return policy, nil
}

func (c *clients) keyRolesHeld(ctx context.Context, class edge.Class, member string, roles []string) (bool, error) {
	policy, err := c.keyPolicy(ctx, class)
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

func (c *clients) bindKeyRoles(ctx context.Context, class edge.Class, member string, roles, wanted []string) (bool, error) {
	policy, err := c.keyPolicy(ctx, class)
	if err != nil || policy == nil {
		return false, err
	}
	bindings, changed := boundKeyRoles(policy.GetBindings(), member, roles, wanted)
	if !changed {
		return false, nil
	}
	client, err := c.KMS()
	if err != nil {
		return false, err
	}
	policy.Bindings = bindings
	if _, err := dialled(ctx, func() (*iampb.Policy, error) {
		return client.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{Resource: keyPath(c, string(class)), Policy: policy})
	}); err != nil {
		return false, fmt.Errorf("hold %s to %v on the %s key: %w", member, wanted, class, err)
	}
	return true, nil
}

func boundMember(bindings []*cloudresourcemanager.Binding, role, member string,
	condition *cloudresourcemanager.Expr, granting bool) ([]*cloudresourcemanager.Binding, bool) {
	for _, binding := range bindings {
		if binding.Role != role || !sameCondition(binding.Condition, condition) {
			continue
		}
		members, changed := boundMembers(binding.Members, member, granting)
		binding.Members = members
		return bindings, changed
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
		var held bool
		bindings, held = boundKeyMember(bindings, role, member, slices.Contains(wanted, role))
		changed = changed || held
	}
	return bindings, changed
}

func boundKeyMember(bindings []*iampb.Binding, role, member string, granting bool) ([]*iampb.Binding, bool) {
	for _, binding := range bindings {
		if binding.GetRole() != role {
			continue
		}
		members, changed := boundMembers(binding.GetMembers(), member, granting)
		binding.Members = members
		return bindings, changed
	}
	if !granting {
		return bindings, false
	}
	return append(bindings, &iampb.Binding{Role: role, Members: []string{member}}), true
}
