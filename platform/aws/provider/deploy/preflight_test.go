package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func preflightConfig() Config {
	return Config{
		Env:           "prod",
		StateTable:    "ocel-state",
		StateTableARN: "arn:aws:dynamodb:us-east-1:123456789012:table/ocel-state",
	}
}

func preflightPlan() providerkit.DeployPlan {
	return providerkit.DeployPlan{
		Slug:  "shop",
		Class: providerkit.ClassProduction,
		Env:   "prod",
		Apps:  []providerkit.AppEntry{{App: "web"}, {App: "docs"}},
	}
}

func preflighting(cfg Config, pre providerkit.DeployPreflight) error {
	return newReleaser(fixed(cfg), &Realized{}, nil).Preflight(context.Background(), pre)
}

func TestPreflightPolicyBudget(t *testing.T) {
	t.Parallel()

	t.Run("a bill within budget passes", func(t *testing.T) {
		t.Parallel()

		uploads := []providerkit.Resource{{Name: "bucket--uploads", Declared: "uploads", Type: providerkit.LinkBucket}}
		pre := providerkit.DeployPreflight{
			Plan:      preflightPlan(),
			Resources: uploads,
			Apps: []providerkit.AppUsage{
				{App: "web", Resources: uploads},
				{App: "docs", Resources: uploads},
			},
		}
		if err := preflighting(preflightConfig(), pre); err != nil {
			t.Fatalf("Preflight() = %v, want one bucket to fit", err)
		}
	})

	t.Run("buckets this deploy has not stood up yet are billed at the widest name AWS hands out", func(t *testing.T) {
		t.Parallel()

		pre := providerkit.DeployPreflight{
			Plan: preflightPlan(),
			Apps: []providerkit.AppUsage{{App: "web"}, {App: "docs"}},
		}
		for i := range 40 {
			name := fmt.Sprintf("bucket--%02d", i)
			held := providerkit.Resource{Name: name, Declared: name, Type: providerkit.LinkBucket}
			pre.Resources = append(pre.Resources, held)
			pre.Apps[0].Resources = append(pre.Apps[0].Resources, held)
			pre.Apps[1].Resources = append(pre.Apps[1].Resources, held)
		}

		var over *PolicyBudgetError
		if err := preflighting(preflightConfig(), pre); !errors.As(err, &over) {
			t.Fatalf("Preflight() = %v, want a *PolicyBudgetError", err)
		}
		if len(over.Apps) != 2 || over.Apps[0].App != "web" || over.Apps[1].App != "docs" {
			t.Errorf("billed %+v, want every app whose role would carry the bill", over.Apps)
		}
		if !strings.Contains(over.Error(), "bucket--00") {
			t.Errorf("Error() = %q, want it to name the resources it billed", over.Error())
		}
	})

	t.Run("a link already published is billed from the grants it carries", func(t *testing.T) {
		t.Parallel()

		pre := providerkit.DeployPreflight{
			Plan: preflightPlan(),
			Apps: []providerkit.AppUsage{{App: "web"}, {App: "docs"}},
		}
		for i := range 40 {
			name := fmt.Sprintf("bucket--%02d", i)
			pre.Grants = append(pre.Grants, providerkit.Link{
				Name: name,
				Type: providerkit.LinkBucket,
				Grants: []providerkit.Grant{{
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

		pre := providerkit.DeployPreflight{
			Plan: preflightPlan(),
			Apps: []providerkit.AppUsage{{App: "web"}, {App: "docs"}},
		}
		for i := range 40 {
			name := fmt.Sprintf("bucket--%02d", i)
			held := providerkit.Resource{Name: name, Declared: name, Type: providerkit.LinkBucket}
			pre.Resources = append(pre.Resources, held)
			pre.Apps[0].Resources = append(pre.Apps[0].Resources, held)
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

	t.Run("a declared bucket and its published link are one line on the bill", func(t *testing.T) {
		t.Parallel()

		items, err := billedPolicies(
			[]providerkit.Resource{{Name: "bucket--uploads", Declared: "uploads", Type: providerkit.LinkBucket}},
			[]providerkit.Link{{
				Name:   "bucket--uploads",
				Type:   providerkit.LinkBucket,
				Grants: []providerkit.Grant{{Label: "objects", Actions: []string{"s3:GetObject"}, Resources: []string{"arn:aws:s3:::uploads/*"}}},
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
