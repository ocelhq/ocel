package bootstrap

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/runui"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func bootstrapOf(stacks ...*contractv1.BootstrapStack) *contractv1.BootstrapStatus {
	return &contractv1.BootstrapStatus{
		Tier:           environmentv1.Tier_TIER_PRODUCTION,
		Present:        true,
		Schema:         1,
		RequiredSchema: 1,
		Stacks:         stacks,
	}
}

func TestPlanBootstrap(t *testing.T) {
	core := &contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true, Required: true}

	tests := []struct {
		name     string
		status   *contractv1.BootstrapStatus
		missing  []string
		stale    []string
		features []string
	}{
		{
			name:   "a bootstrap nothing has been deployed into asks for nothing",
			status: &contractv1.BootstrapStatus{Tier: environmentv1.Tier_TIER_PRODUCTION},
		},
		{
			name: "everything this project needs is there and current",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
			),
			features: []string{"isr"},
		},
		{
			name: "a required feature that is not there is added",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization", Required: true},
			),
			missing:  []string{"image-optimization"},
			features: []string{"image-optimization", "isr"},
		},
		{
			name: "a required feature that has fallen behind is refreshed",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Required: true},
			),
			stale:    []string{"ocel-bootstrap-isr"},
			features: []string{"isr"},
		},
		{
			name: "the core falling behind is a refresh of its own",
			status: bootstrapOf(
				&contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, Required: true},
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
			),
			stale:    []string{"ocel-bootstrap"},
			features: []string{"isr"},
		},
		{
			name: "a feature no project here needs is neither added nor refreshed",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true},
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization"},
			),
			features: []string{"isr"},
		},
		{
			name: "one set covers both what is missing and what is behind",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Required: true},
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization", Required: true},
			),
			missing:  []string{"image-optimization"},
			stale:    []string{"ocel-bootstrap-isr"},
			features: []string{"image-optimization", "isr"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanFor(tt.status)
			if !slices.Equal(plan.Missing, tt.missing) {
				t.Errorf("missing = %v, want %v", plan.Missing, tt.missing)
			}
			if !slices.Equal(plan.Stale, tt.stale) {
				t.Errorf("stale = %v, want %v", plan.Stale, tt.stale)
			}
			if !slices.Equal(plan.Features, tt.features) {
				t.Errorf("features = %v, want %v", plan.Features, tt.features)
			}
			if plan.Empty() != (len(tt.missing) == 0 && len(tt.stale) == 0) {
				t.Errorf("empty() = %t for %v/%v", plan.Empty(), plan.Missing, plan.Stale)
			}
		})
	}
}

func TestOfferedBootstrapCarriesTheEdgeTheProjectChose(t *testing.T) {
	plan := Plan{Features: []string{"isr"}, Missing: []string{"isr"}}
	front := &contractv1.EdgeSelection{Kind: "cloudflare"}

	req := plan.Request(environmentv1.Tier_TIER_PREVIEW, front)
	if req.GetEdge().GetKind() != "cloudflare" {
		t.Errorf("request edge = %q, want the edge the project chose", req.GetEdge().GetKind())
	}
	if req.GetTier() != environmentv1.Tier_TIER_PREVIEW || !slices.Equal(req.GetFeatures(), plan.Features) {
		t.Errorf("request = %v, want the offered plan's own tier and features", req)
	}
}

func TestOfferBootstrapWithoutATerminal(t *testing.T) {
	core := &contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true, Required: true}

	t.Run("a missing feature stops the run and names the command", func(t *testing.T) {
		status := bootstrapOf(core,
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization", Required: true},
		)
		var out bytes.Buffer
		err := Offer(context.Background(), nil, status, environmentv1.Tier_TIER_PRODUCTION, nil, runui.Plain(runui.Presentation{}, &out), false, &out, nil)
		if err == nil {
			t.Fatal("a deploy against a bootstrap missing a feature it needs was allowed through")
		}
		if !strings.Contains(err.Error(), "ocel bootstrap production --features image-optimization,isr") {
			t.Errorf("refusal = %q, want the literal command to run", err)
		}
	})

	t.Run("a stale stack warns and lets the deploy through", func(t *testing.T) {
		status := bootstrapOf(core,
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Required: true},
		)
		var out bytes.Buffer
		if err := Offer(context.Background(), nil, status, environmentv1.Tier_TIER_PREVIEW, nil, runui.Plain(runui.Presentation{}, &out), false, &out, nil); err != nil {
			t.Fatalf("a bootstrap that is merely behind stopped the deploy: %v", err)
		}
		for _, want := range []string{"ocel-bootstrap-isr", "ocel bootstrap preview --features isr"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("stdout = %q, want it to carry %q", out.String(), want)
			}
		}
	})

	t.Run("a bootstrap that carries what this project needs says nothing", func(t *testing.T) {
		status := bootstrapOf(core,
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
		)
		var out bytes.Buffer
		if err := Offer(context.Background(), nil, status, environmentv1.Tier_TIER_PRODUCTION, nil, runui.Plain(runui.Presentation{}, &out), false, &out, nil); err != nil {
			t.Fatalf("offerBootstrap err = %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("stdout = %q, want nothing said about a bootstrap that is what it should be", out.String())
		}
	})
}
