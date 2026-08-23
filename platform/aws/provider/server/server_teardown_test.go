package server

import (
	"context"
	"slices"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestClassOf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		tier environmentv1.Tier
		want string
	}{
		{name: "an unspecified class is the production bootstrap", tier: environmentv1.Tier_TIER_UNSPECIFIED, want: bootstrap.ClassProduction},
		{name: "production", tier: environmentv1.Tier_TIER_PRODUCTION, want: bootstrap.ClassProduction},
		{name: "preview", tier: environmentv1.Tier_TIER_PREVIEW, want: bootstrap.ClassPreview},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := classOf(tc.tier)
			if err != nil {
				t.Fatalf("classOf(%v): %v", tc.tier, err)
			}
			if got != tc.want {
				t.Errorf("classOf(%v) = %q, want %q", tc.tier, got, tc.want)
			}
		})
	}

	t.Run("a class this build does not know is not a bootstrap", func(t *testing.T) {
		t.Parallel()

		if _, err := classOf(environmentv1.Tier(99)); err == nil {
			t.Error("a class naming no bootstrap, want a refusal")
		}
	})
}

func TestBootstrapOccupancyRefuse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		class     string
		occupancy bootstrapOccupancy
		wantOK    bool
		wants     []string
	}{
		{
			name:      "an empty bootstrap is free to go",
			class:     bootstrap.ClassProduction,
			occupancy: bootstrapOccupancy{},
			wantOK:    true,
		},
		{
			name:      "live projects are named, one command each",
			class:     bootstrap.ClassProduction,
			occupancy: bootstrapOccupancy{projects: []string{"shop", "docs"}},
			wants:     []string{"shop", "docs", "ocel destroy"},
		},
		{
			name:      "a preview wildcard is named with the command that releases it",
			class:     bootstrap.ClassPreview,
			occupancy: bootstrapOccupancy{wildcard: "preview.acme.com"},
			wants:     []string{"*.preview.acme.com", "ocel domain release --preview"},
		},
		{
			name:      "both are reported together",
			class:     bootstrap.ClassPreview,
			occupancy: bootstrapOccupancy{projects: []string{"shop"}, wildcard: "preview.acme.com"},
			wants:     []string{"shop", "ocel destroy --preview", "*.preview.acme.com", "ocel domain release --preview"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.occupancy.refuse(tc.class)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("refuse() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("refuse() = nil, want a refusal")
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal must name %q, got %q", want, err)
				}
			}
		})
	}
}

func TestReadBootstrapOccupancy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ssmc := &stateSSM{params: map[string]string{}}
	if err := bootstrap.WriteStackRecordFor(ctx, ssmc, bootstrap.ClassPreview, "shop", bootstrap.StackRecord{Edge: edge.StackState{Slug: "shop"}}); err != nil {
		t.Fatalf("WriteStackStateFor: %v", err)
	}
	if err := bootstrap.WritePreviewDomain(ctx, ssmc, bootstrap.ClassPreview, bootstrap.PreviewDomain{BaseDomain: "preview.acme.com"}); err != nil {
		t.Fatalf("WritePreviewDomain: %v", err)
	}

	deps := teardownDeps{ssm: ssmc, index: &teardownIndex{projects: []string{"shop", "docs"}}}
	got, err := readBootstrapOccupancy(ctx, deps, bootstrap.ClassPreview)
	if err != nil {
		t.Fatalf("readBootstrapOccupancy: %v", err)
	}
	if !slices.Equal(got.projects, []string{"docs", "shop"}) {
		t.Errorf("projects = %v, want the stack index and the recorded edge stacks merged", got.projects)
	}
	if got.wildcard != "preview.acme.com" {
		t.Errorf("wildcard = %q, want the recorded base domain", got.wildcard)
	}

	production, err := readBootstrapOccupancy(ctx, teardownDeps{ssm: ssmc}, bootstrap.ClassProduction)
	if err != nil {
		t.Fatalf("readBootstrapOccupancy(production): %v", err)
	}
	if len(production.projects) != 0 || production.wildcard != "" {
		t.Errorf("production occupancy = %+v, want nothing: a preview stack is not a production one", production)
	}
}

type teardownIndex struct {
	projects []string
	err      error
}

func (i *teardownIndex) Projects(context.Context) ([]string, error) { return i.projects, i.err }
