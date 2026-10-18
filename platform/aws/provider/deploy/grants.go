package deploy

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	rolePolicyCeilingChars     = 10240
	platformPolicyReserveChars = 4608
	policyBudgetChars          = rolePolicyCeilingChars - platformPolicyReserveChars

	s3ARNPrefix = "arn:aws:s3:::"

	widestEventAPIARN = "arn:aws:appsync:ap-southeast-5:123456789012:apis/abcdefghijklmnopqrstuvwxyz"

	widestMasterSecretARN = "arn:aws:secretsmanager:ap-southeast-5:123456789012:secret:rds!cluster-01234567-89ab-cdef-0123-456789abcdef-AbCdEf"
)

type bindingPolicy struct {
	Binding string
	Policy  string
}

func bucketGrants(bucket string, sessions sessionScope) []*variablestorev1.Grant {
	arn := s3ARNPrefix + bucket
	return []*variablestorev1.Grant{
		{
			Label: "objects",
			Actions: []string{
				"s3:AbortMultipartUpload",
				"s3:DeleteObject",
				"s3:GetObject",
				"s3:ListMultipartUploadParts",
				"s3:PutObject",
				"s3:PutObjectTagging",
			},
			Resources: []string{arn + "/*"},
		},
		{
			Label:     "listing",
			Actions:   []string{"s3:ListBucket", "s3:ListBucketMultipartUploads"},
			Resources: []string{arn},
		},
		{
			Label:     "sessions",
			Actions:   []string{"dynamodb:GetItem", "dynamodb:PutItem"},
			Resources: []string{sessions.TableARN},
			Conditions: []*variablestorev1.GrantCondition{{
				Operator: "ForAllValues:StringLike",
				Key:      "dynamodb:LeadingKeys",
				Values:   []string{sessions.KeyPrefix + "*"},
			}},
		},
	}
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

func VerifyGrants(binding provider.Binding) error {
	for _, grant := range binding.Grants {
		if err := scoped(binding.Name, grant.Label, grant.Actions, grant.Resources); err != nil {
			return err
		}
	}
	return nil
}

func checkGrant(binding string, grant *variablestorev1.Grant) error {
	return scoped(binding, grant.GetLabel(), grant.GetActions(), grant.GetResources())
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

func bindingPolicyDocument(name string, grants []*variablestorev1.Grant) (string, error) {
	if len(grants) == 0 {
		return "", nil
	}
	statements := make([]map[string]any, 0, len(grants))
	for _, grant := range grants {
		if err := checkGrant(name, grant); err != nil {
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
	out, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	if err != nil {
		return "", fmt.Errorf("render the inline policy for binding %s: %w", name, err)
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

type PolicyBillItem struct {
	Binding string
	Type    provider.BindingType
	Chars   int
}

type PolicyBudgetApp struct {
	App   string
	Total int
	Items []PolicyBillItem
}

type PolicyBudgetError struct {
	Apps []PolicyBudgetApp
}

func (e *PolicyBudgetError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"AWS caps a role's inline policies at %d characters, of which Ocel's own runtime policies use %d, leaving %d for the bindings an app uses:\n",
		rolePolicyCeilingChars, platformPolicyReserveChars, policyBudgetChars,
	)
	for _, app := range e.Apps {
		fmt.Fprintf(&b,
			"\napp %s binds %d resources whose inline IAM policies come to %d characters on one execution role:\n",
			app.App, len(app.Items), app.Total,
		)
		for _, item := range app.Items {
			fmt.Fprintf(&b, "\n  %s  %s  %d characters", item.Binding, item.Type, item.Chars)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nSplit the app, or stop reaching for the resources it does not need; a resource an app never uses costs it nothing.")
	return b.String()
}

func checkInlinePolicyBudget(apps []provider.AppUsage, sessions sessionScope) error {
	var over []PolicyBudgetApp
	for _, app := range apps {
		items, err := billedPolicies(app.Resources, app.Grants, sessions)
		if err != nil {
			return err
		}
		total := 0
		for _, item := range items {
			total += item.Chars
		}
		if total <= policyBudgetChars {
			continue
		}
		slices.SortFunc(items, func(a, b PolicyBillItem) int {
			if c := cmp.Compare(b.Chars, a.Chars); c != 0 {
				return c
			}
			return cmp.Compare(a.Binding, b.Binding)
		})
		over = append(over, PolicyBudgetApp{App: app.App, Total: total, Items: items})
	}
	if len(over) == 0 {
		return nil
	}
	return &PolicyBudgetError{Apps: over}
}

func billedPolicies(resources []provider.Resource, grants []provider.Binding, sessions sessionScope) ([]PolicyBillItem, error) {
	billed := map[string]PolicyBillItem{}
	for _, binding := range grants {
		policy, err := bindingPolicyDocument(binding.Name, provider.GrantMessages(binding.Grants))
		if err != nil {
			return nil, err
		}
		if policy == "" {
			continue
		}
		billed[binding.Name] = PolicyBillItem{Binding: binding.Name, Type: binding.Type, Chars: len(policy)}
	}
	for _, resource := range resources {
		if resource.Binding != "" {
			continue
		}
		var grants []*variablestorev1.Grant
		switch resource.Type {
		case provider.BindingBucket:
			grants = bucketGrants(strings.Repeat("b", maxS3BucketNameLen), sessions)
		case provider.BindingRealtime:
			grants = realtimeGrants(widestEventAPIARN, resource.Declared)
		case provider.BindingPostgres:
			grants = postgresGrants(widestMasterSecretARN)
		default:
			continue
		}
		policy, err := bindingPolicyDocument(resource.Name, grants)
		if err != nil {
			return nil, err
		}
		billed[resource.Name] = PolicyBillItem{Binding: resource.Name, Type: resource.Type, Chars: len(policy)}
	}
	items := make([]PolicyBillItem, 0, len(billed))
	for _, name := range slices.Sorted(maps.Keys(billed)) {
		items = append(items, billed[name])
	}
	return items, nil
}
