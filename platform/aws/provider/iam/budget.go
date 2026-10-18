package iam

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	RolePolicyCeilingChars     = 10240
	PlatformPolicyReserveChars = 4608
	PolicyBudgetChars          = RolePolicyCeilingChars - PlatformPolicyReserveChars
)

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
		RolePolicyCeilingChars, PlatformPolicyReserveChars, PolicyBudgetChars,
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
