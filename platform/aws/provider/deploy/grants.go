package deploy

import (
	"cmp"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

type bindingPolicy struct {
	Binding string
	Policy  string
}

func checkInlinePolicyBudget(apps []provider.AppUsage, registry component.Registry, at component.Stack) error {
	var over []iam.PolicyBudgetApp
	for _, app := range apps {
		items, err := billedPolicies(app.Resources, app.Grants, registry, at)
		if err != nil {
			return err
		}
		total := 0
		for _, item := range items {
			total += item.Chars
		}
		if total <= iam.PolicyBudgetChars {
			continue
		}
		slices.SortFunc(items, func(a, b iam.PolicyBillItem) int {
			if c := cmp.Compare(b.Chars, a.Chars); c != 0 {
				return c
			}
			return cmp.Compare(a.Binding, b.Binding)
		})
		over = append(over, iam.PolicyBudgetApp{App: app.App, Total: total, Items: items})
	}
	if len(over) == 0 {
		return nil
	}
	return &iam.PolicyBudgetError{Apps: over}
}

func billedPolicies(resources []provider.Resource, grants []provider.Binding, registry component.Registry, at component.Stack) ([]iam.PolicyBillItem, error) {
	billed := map[string]iam.PolicyBillItem{}
	for _, binding := range grants {
		policy, err := iam.GrantPolicy(binding.Name, provider.GrantMessages(binding.Grants))
		if err != nil {
			return nil, err
		}
		if policy == "" {
			continue
		}
		billed[binding.Name] = iam.PolicyBillItem{Binding: binding.Name, Type: binding.Type, Chars: len(policy)}
	}
	for _, resource := range resources {
		if resource.Binding != "" {
			continue
		}
		granting, served := registry.For(resource.Type)
		if !served || granting.WidestGrants == nil {
			continue
		}
		policy, err := iam.GrantPolicy(resource.Name, granting.WidestGrants(at, resource))
		if err != nil {
			return nil, err
		}
		billed[resource.Name] = iam.PolicyBillItem{Binding: resource.Name, Type: resource.Type, Chars: len(policy)}
	}
	items := make([]iam.PolicyBillItem, 0, len(billed))
	for _, name := range slices.Sorted(maps.Keys(billed)) {
		items = append(items, billed[name])
	}
	return items, nil
}
