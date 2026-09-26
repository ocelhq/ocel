package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func preflightConfig() Config {
	return Config{
		Env:           "prod",
		StateTable:    "ocel-state",
		StateTableARN: "arn:aws:dynamodb:us-east-1:123456789012:table/ocel-state",
	}
}

func preflightSpec() provider.DeploySpec {
	return provider.DeploySpec{
		Slug:  "shop",
		Class: edge.ClassProduction,
		Env:   "prod",
		Apps:  []provider.AppEntry{{App: "web"}, {App: "docs"}},
	}
}

func preflighting(cfg Config, pre provider.DeployPreflight) error {
	return newStacks(fixed(cfg), &Realized{}, nil).Preflight(context.Background(), pre)
}

func TestPreflightPolicyBudget(t *testing.T) {
	t.Parallel()

	t.Run("a bill within budget passes", func(t *testing.T) {
		t.Parallel()

		uploads := []provider.Resource{{Name: "bucket--uploads", Declared: "uploads", Type: provider.BindingBucket}}
		pre := provider.DeployPreflight{
			Deploy:    preflightSpec(),
			Resources: uploads,
			Apps: []provider.AppUsage{
				{App: "web", Resources: uploads},
				{App: "docs", Resources: uploads},
			},
		}
		if err := preflighting(preflightConfig(), pre); err != nil {
			t.Fatalf("Preflight() = %v, want one bucket to fit", err)
		}
	})

	t.Run("buckets this deploy has not provisioned yet are billed at the widest name AWS hands out", func(t *testing.T) {
		t.Parallel()

		pre := provider.DeployPreflight{
			Deploy: preflightSpec(),
			Apps:   []provider.AppUsage{{App: "web"}, {App: "docs"}},
		}
		for i := range 40 {
			name := fmt.Sprintf("bucket--%02d", i)
			bucket := provider.Resource{Name: name, Declared: name, Type: provider.BindingBucket}
			pre.Resources = append(pre.Resources, bucket)
			pre.Apps[0].Resources = append(pre.Apps[0].Resources, bucket)
			pre.Apps[1].Resources = append(pre.Apps[1].Resources, bucket)
		}

		var over *PolicyBudgetError
		if err := preflighting(preflightConfig(), pre); !errors.As(err, &over) {
			t.Fatalf("Preflight() = %v, want a *PolicyBudgetError", err)
		}
		if len(over.Apps) != 2 || over.Apps[0].App != "web" || over.Apps[1].App != "docs" {
			t.Errorf("billed %+v, want every app whose role would bear the bill", over.Apps)
		}
		if !strings.Contains(over.Error(), "bucket--00") {
			t.Errorf("Error() = %q, want it to name the resources it billed", over.Error())
		}
	})

	t.Run("a binding already published is billed from the grants it lists", func(t *testing.T) {
		t.Parallel()

		pre := provider.DeployPreflight{
			Deploy: preflightSpec(),
			Apps:   []provider.AppUsage{{App: "web"}, {App: "docs"}},
		}
		for i := range 40 {
			name := fmt.Sprintf("bucket--%02d", i)
			pre.Grants = append(pre.Grants, provider.Binding{
				Name: name,
				Type: provider.BindingBucket,
				Grants: []provider.Grant{{
					Label:     "objects",
					Actions:   []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject"},
					Resources: []string{"arn:aws:s3:::" + strings.Repeat("b", 50) + name + "/*"},
				}},
			})
		}
		pre.Apps[0].Grants = pre.Grants
		pre.Apps[1].Grants = pre.Grants

		var over *PolicyBudgetError
		if err := preflighting(preflightConfig(), pre); !errors.As(err, &over) {
			t.Fatalf("Preflight() = %v, want a *PolicyBudgetError", err)
		}
	})

	t.Run("only the app whose own role is over budget is refused", func(t *testing.T) {
		t.Parallel()

		pre := provider.DeployPreflight{
			Deploy: preflightSpec(),
			Apps:   []provider.AppUsage{{App: "web"}, {App: "docs"}},
		}
		for i := range 40 {
			name := fmt.Sprintf("bucket--%02d", i)
			bucket := provider.Resource{Name: name, Declared: name, Type: provider.BindingBucket}
			pre.Resources = append(pre.Resources, bucket)
			pre.Apps[0].Resources = append(pre.Apps[0].Resources, bucket)
		}
		pre.Apps[1].Resources = pre.Apps[0].Resources[:1]

		var over *PolicyBudgetError
		if err := preflighting(preflightConfig(), pre); !errors.As(err, &over) {
			t.Fatalf("Preflight() = %v, want a *PolicyBudgetError", err)
		}
		if len(over.Apps) != 1 || over.Apps[0].App != "web" {
			t.Fatalf("billed %+v, want only web: docs uses one bucket, and a resource an app never uses costs it nothing", over.Apps)
		}
	})

	t.Run("a declared bucket and its published binding are one line on the bill", func(t *testing.T) {
		t.Parallel()

		items, err := billedPolicies(
			[]provider.Resource{{Name: "bucket--uploads", Declared: "uploads", Type: provider.BindingBucket}},
			[]provider.Binding{{
				Name:   "bucket--uploads",
				Type:   provider.BindingBucket,
				Grants: []provider.Grant{{Label: "objects", Actions: []string{"s3:GetObject"}, Resources: []string{"arn:aws:s3:::uploads/*"}}},
			}},
			newSessionScope("shop", "prod", preflightConfig().StateTableARN),
		)
		if err != nil {
			t.Fatalf("billedPolicies() = %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("billedPolicies() = %+v, want the bucket billed once", items)
		}
	})
}
