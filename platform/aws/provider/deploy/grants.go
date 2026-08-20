package deploy

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
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

func checkLinkGrants(links []*linksv1.Link) error {
	for _, link := range links {
		for _, grant := range link.GetGrants() {
			if err := checkGrant(link.GetName(), grant); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkGrant(link string, grant *linksv1.Grant) error {
	label := grant.GetLabel()
	if label == "" {
		label = "unlabelled"
	}
	if len(grant.GetActions()) == 0 || slices.ContainsFunc(grant.GetActions(), vars.UnscopedAction) {
		return &UnscopedGrantError{Link: link, Label: label, Field: "action set"}
	}
	if len(grant.GetResources()) == 0 || slices.ContainsFunc(grant.GetResources(), vars.UnscopedResource) {
		return &UnscopedGrantError{Link: link, Label: label, Field: "resource set"}
	}
	return nil
}

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

func appLinkPolicies(manifest *contractv1.Manifest, app string, links []*linksv1.Link) ([]linkPolicy, error) {
	used := usedResources(manifest, app)
	out := make([]linkPolicy, 0, len(used))
	for _, link := range links {
		if !used[link.GetName()] {
			continue
		}
		policy, err := linkPolicyDocument(link.GetName(), link.GetGrants())
		if err != nil {
			return nil, err
		}
		if policy == "" {
			continue
		}
		out = append(out, linkPolicy{Link: link.GetName(), Policy: policy})
	}
	slices.SortFunc(out, func(a, b linkPolicy) int { return cmp.Compare(a.Link, b.Link) })
	return out, nil
}

func usedResources(manifest *contractv1.Manifest, app string) map[string]bool {
	used := map[string]bool{}
	for _, usage := range manifest.GetUsages() {
		if usage.GetApp() == app {
			used[usage.GetResource()] = true
		}
	}
	return used
}

func billedResourcePolicy(r *contractv1.ManifestResource, consumed map[string]Consumed, sessions sessionScope) (string, error) {
	if r.GetLinked() {
		return linkPolicyDocument(r.GetLogicalName(), publishedGrants(consumed[r.GetLogicalName()]))
	}
	if r.GetBucket() == nil {
		return "", nil
	}
	return linkPolicyDocument(r.GetLogicalName(), bucketGrants(strings.Repeat("b", maxS3BucketNameLen), sessions))
}

func publishedGrants(c Consumed) []*linksv1.Grant {
	return c.Record.Link.GetGrants()
}

type PolicyBillItem struct {
	Link  string
	Type  linksv1.LinkType
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

func checkInlinePolicyBudget(manifest *contractv1.Manifest, consumed map[string]Consumed, sessions sessionScope) error {
	costs := make(map[string]PolicyBillItem, len(manifest.GetResources()))
	for _, r := range manifest.GetResources() {
		policy, err := billedResourcePolicy(r, consumed, sessions)
		if err != nil {
			return err
		}
		if policy == "" {
			continue
		}
		costs[r.GetLogicalName()] = PolicyBillItem{
			Link:  r.GetLogicalName(),
			Type:  r.GetResource().GetType(),
			Chars: len(policy),
		}
	}

	bills := map[string][]PolicyBillItem{}
	billed := map[[2]string]bool{}
	for _, usage := range manifest.GetUsages() {
		item, ok := costs[usage.GetResource()]
		if !ok {
			continue
		}
		pair := [2]string{usage.GetApp(), usage.GetResource()}
		if billed[pair] {
			continue
		}
		billed[pair] = true
		bills[usage.GetApp()] = append(bills[usage.GetApp()], item)
	}

	var over []PolicyBudgetApp
	for _, app := range slices.Sorted(maps.Keys(bills)) {
		items := bills[app]
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
			return cmp.Compare(a.Link, b.Link)
		})
		over = append(over, PolicyBudgetApp{App: app, Total: total, Items: items})
	}
	if len(over) == 0 {
		return nil
	}
	return &PolicyBudgetError{Apps: over}
}
