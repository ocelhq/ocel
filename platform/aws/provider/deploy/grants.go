package deploy

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	rolePolicyCeilingChars     = 10240
	platformPolicyReserveChars = 4608
	policyBudgetChars          = rolePolicyCeilingChars - platformPolicyReserveChars

	s3ARNPrefix = "arn:aws:s3:::"
)

type linkPolicy struct {
	Link   string
	Policy string
}

func bucketGrants(bucket string, sessions sessionScope) []*linksv1.Grant {
	arn := s3ARNPrefix + bucket
	return []*linksv1.Grant{
		{
			Label:     "objects",
			Actions:   []string{"s3:DeleteObject", "s3:GetObject", "s3:PutObject", "s3:PutObjectTagging"},
			Resources: []string{arn + "/*"},
		},
		{
			Label:     "listing",
			Actions:   []string{"s3:ListBucket"},
			Resources: []string{arn},
		},
		{
			Label:     "sessions",
			Actions:   []string{"dynamodb:GetItem", "dynamodb:PutItem"},
			Resources: []string{sessions.TableARN},
			Conditions: []*linksv1.GrantCondition{{
				Operator: "ForAllValues:StringLike",
				Key:      "dynamodb:LeadingKeys",
				Values:   []string{sessions.KeyPrefix + "*"},
			}},
		},
	}
}

type UnscopedGrantError struct {
	Link  string
	Label string
	Field string
}

func (e *UnscopedGrantError) Error() string {
	return fmt.Sprintf(
		"link %s carries a grant (%s) with an unscoped %s. "+
			"Ocel renders one inline policy per link and refuses blanket access: name the actions and the resource ARNs the app needs",
		e.Link, e.Label, e.Field,
	)
}

func (e *UnscopedGrantError) Unwrap() error { return providerkit.ErrUnscopedGrant }

func VerifyGrants(link providerkit.Link) error {
	for _, grant := range link.Grants {
		if err := scoped(link.Name, grant.Label, grant.Actions, grant.Resources); err != nil {
			return err
		}
	}
	return nil
}

func checkGrant(link string, grant *linksv1.Grant) error {
	return scoped(link, grant.GetLabel(), grant.GetActions(), grant.GetResources())
}

func scoped(link, label string, actions, resources []string) error {
	if label == "" {
		label = "unlabelled"
	}
	if len(actions) == 0 || slices.ContainsFunc(actions, unscopedAction) {
		return &UnscopedGrantError{Link: link, Label: label, Field: "action set"}
	}
	if len(resources) == 0 || slices.ContainsFunc(resources, unscopedResource) {
		return &UnscopedGrantError{Link: link, Label: label, Field: "resource set"}
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

func linkPolicyDocument(name string, grants []*linksv1.Grant) (string, error) {
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
		return "", fmt.Errorf("render the inline policy for link %s: %w", name, err)
	}
	return string(out), nil
}

func grantCondition(grant *linksv1.Grant) map[string]any {
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
	Link  string
	Type  providerkit.LinkType
	Chars int
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
		"AWS caps a role's inline policies at %d characters, of which Ocel's own runtime policies hold %d, leaving %d for the links an app uses:\n",
		rolePolicyCeilingChars, platformPolicyReserveChars, policyBudgetChars,
	)
	for _, app := range e.Apps {
		fmt.Fprintf(&b,
			"\napp %s links %d resources whose inline IAM policies come to %d characters on one execution role:\n",
			app.App, len(app.Items), app.Total,
		)
		for _, item := range app.Items {
			fmt.Fprintf(&b, "\n  %s  %s  %d characters", item.Link, item.Type, item.Chars)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nSplit the app, or stop reaching for the resources it does not need; a resource an app never uses costs it nothing.")
	return b.String()
}

func checkInlinePolicyBudget(plan providerkit.DeployPlan, resources []providerkit.Resource, grants []providerkit.Link, sessions sessionScope) error {
	items, err := billedPolicies(resources, grants, sessions)
	if err != nil {
		return err
	}
	total := 0
	for _, item := range items {
		total += item.Chars
	}
	if total <= policyBudgetChars {
		return nil
	}
	slices.SortFunc(items, func(a, b PolicyBillItem) int {
		if c := cmp.Compare(b.Chars, a.Chars); c != 0 {
			return c
		}
		return cmp.Compare(a.Link, b.Link)
	})
	over := make([]PolicyBudgetApp, 0, len(plan.Apps))
	for _, app := range plan.Apps {
		over = append(over, PolicyBudgetApp{App: app.App, Total: total, Items: items})
	}
	return &PolicyBudgetError{Apps: over}
}

func billedPolicies(resources []providerkit.Resource, grants []providerkit.Link, sessions sessionScope) ([]PolicyBillItem, error) {
	billed := map[string]PolicyBillItem{}
	for _, link := range grants {
		policy, err := linkPolicyDocument(link.Name, grantMessages(link.Grants))
		if err != nil {
			return nil, err
		}
		if policy == "" {
			continue
		}
		billed[link.Name] = PolicyBillItem{Link: link.Name, Type: link.Type, Chars: len(policy)}
	}
	for _, resource := range resources {
		if resource.Linked || resource.Type != providerkit.LinkBucket {
			continue
		}
		policy, err := linkPolicyDocument(resource.Name, bucketGrants(strings.Repeat("b", maxS3BucketNameLen), sessions))
		if err != nil {
			return nil, err
		}
		billed[resource.Name] = PolicyBillItem{Link: resource.Name, Type: resource.Type, Chars: len(policy)}
	}
	items := make([]PolicyBillItem, 0, len(billed))
	for _, name := range slices.Sorted(maps.Keys(billed)) {
		items = append(items, billed[name])
	}
	return items, nil
}
