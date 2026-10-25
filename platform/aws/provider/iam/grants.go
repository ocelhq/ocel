package iam

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type BindingPolicy struct {
	Binding string
	Policy  string
}

type UnscopedGrantError struct {
	Binding string
	Label   string
	Field   string
}

func (e *UnscopedGrantError) Error() string {
	return fmt.Sprintf(
		"binding %s has a grant (%s) with an unscoped %s. "+
			"Ocel renders one inline policy per binding and refuses blanket access: name the actions and the resource ARNs the app needs",
		e.Binding, e.Label, e.Field,
	)
}

func (e *UnscopedGrantError) Unwrap() error { return provider.ErrUnscopedGrant }

type RoleGrantError struct {
	Binding  string
	Role     string
	Resource string
}

func (e *RoleGrantError) Error() string {
	return fmt.Sprintf(
		"binding %s grants role %s on %s, and AWS grants an app actions over resources, never a role: "+
			"publish the grant as the actions and the resource ARNs the app needs",
		e.Binding, e.Role, e.Resource,
	)
}

func VerifyGrants(binding provider.Binding) error {
	for _, grant := range binding.Grants {
		if grant.Role != "" || grant.Resource != "" {
			return &RoleGrantError{Binding: binding.Name, Role: grant.Role, Resource: grant.Resource}
		}
		if err := scoped(binding.Name, grant.Label, grant.Actions, grant.Resources); err != nil {
			return err
		}
	}
	return nil
}

func scoped(binding, label string, actions, resources []string) error {
	if label == "" {
		label = "unlabelled"
	}
	if len(actions) == 0 || slices.ContainsFunc(actions, unscopedAction) {
		return &UnscopedGrantError{Binding: binding, Label: label, Field: "action set"}
	}
	if len(resources) == 0 || slices.ContainsFunc(resources, unscopedResource) {
		return &UnscopedGrantError{Binding: binding, Label: label, Field: "resource set"}
	}
	return nil
}

const grantWildcard = "*"

func unscopedAction(action string) bool {
	service, verb, named := strings.Cut(action, ":")
	if !named {
		return action == grantWildcard
	}
	return verb == grantWildcard || service == grantWildcard
}

func unscopedResource(resource string) bool { return resource == grantWildcard }

func GrantPolicy(binding string, grants []*variablestorev1.Grant) (string, error) {
	if len(grants) == 0 {
		return "", nil
	}
	statements := make([]map[string]any, 0, len(grants))
	for _, grant := range grants {
		if grant.GetRole() != "" || grant.GetResource() != "" {
			return "", &RoleGrantError{Binding: binding, Role: grant.GetRole(), Resource: grant.GetResource()}
		}
		if err := scoped(binding, grant.GetLabel(), grant.GetActions(), grant.GetResources()); err != nil {
			return "", err
		}
		statement := map[string]any{
			"Effect":   "Allow",
			"Action":   grant.GetActions(),
			"Resource": grant.GetResources(),
		}
		if condition := grantCondition(grant); len(condition) > 0 {
			statement["Condition"] = condition
		}
		statements = append(statements, statement)
	}
	out, err := json.Marshal(map[string]any{"Version": PolicyVersion, "Statement": statements})
	if err != nil {
		return "", fmt.Errorf("render the inline policy for binding %s: %w", binding, err)
	}
	return string(out), nil
}

func grantCondition(grant *variablestorev1.Grant) map[string]any {
	if len(grant.GetConditions()) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, c := range grant.GetConditions() {
		keys, ok := out[c.GetOperator()].(map[string][]string)
		if !ok {
			keys = map[string][]string{}
			out[c.GetOperator()] = keys
		}
		for _, value := range c.GetValues() {
			if !slices.Contains(keys[c.GetKey()], value) {
				keys[c.GetKey()] = append(keys[c.GetKey()], value)
			}
		}
	}
	return out
}

func NewBindingPolicies(grants []provider.Binding) ([]BindingPolicy, error) {
	out := make([]BindingPolicy, 0, len(grants))
	for _, binding := range grants {
		if slices.ContainsFunc(out, func(planned BindingPolicy) bool { return planned.Binding == binding.Name }) {
			continue
		}
		policy, err := GrantPolicy(binding.Name, provider.GrantMessages(binding.Grants))
		if err != nil {
			return nil, err
		}
		if policy == "" {
			continue
		}
		out = append(out, BindingPolicy{Binding: binding.Name, Policy: policy})
	}
	return out, nil
}
