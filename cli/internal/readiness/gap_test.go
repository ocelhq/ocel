package readiness

import (
	"slices"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func bootstrapOf(stacks ...*contractv1.BootstrapStack) *contractv1.BootstrapStatus {
	return &contractv1.BootstrapStatus{
		Tier:    environmentv1.Tier_TIER_PRODUCTION,
		Present: true,
		Stacks:  stacks,
	}
}

func TestTheBootstrapGapHoldsOnlyWhatIsMissingOrStale(t *testing.T) {
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
			gap := NewGap(tt.status)
			if !slices.Equal(gap.Missing, tt.missing) {
				t.Errorf("missing = %v, want %v", gap.Missing, tt.missing)
			}
			if !slices.Equal(gap.Stale, tt.stale) {
				t.Errorf("stale = %v, want %v", gap.Stale, tt.stale)
			}
			if !slices.Equal(gap.Features, tt.features) {
				t.Errorf("features = %v, want %v", gap.Features, tt.features)
			}
			if gap.IsEmpty() != (len(tt.missing) == 0 && len(tt.stale) == 0) {
				t.Errorf("IsEmpty() = %t for %v/%v", gap.IsEmpty(), gap.Missing, gap.Stale)
			}
		})
	}
}

func TestTheRepairRequestSendsTheEdgeTheProjectChose(t *testing.T) {
	gap := Gap{Features: []string{"isr"}, Missing: []string{"isr"}}
	edge := &contractv1.EdgeSelection{Kind: "relay"}

	req := gap.BootstrapRequest(environmentv1.Tier_TIER_PREVIEW, edge)
	if req.GetEdge().GetKind() != "relay" {
		t.Errorf("request edge = %q, want the edge the project chose", req.GetEdge().GetKind())
	}
	if req.GetTier() != environmentv1.Tier_TIER_PREVIEW || !slices.Equal(req.GetFeatures(), gap.Features) {
		t.Errorf("request = %v, want the offered gap's own tier and features", req)
	}
}

func TestABootstrapGapNamesEachFeatureOnce(t *testing.T) {
	status := bootstrapOf(
		&contractv1.BootstrapStack{Name: "cloudflare/ocel-edge-cache", Feature: "cloudflare-edge", Present: true, Required: true},
		&contractv1.BootstrapStack{Name: "cloudflare/ocel-isr-writer", Feature: "cloudflare-edge", Present: true, Required: true},
	)

	gap := NewGap(status)

	if want := []string{"cloudflare/ocel-edge-cache", "cloudflare/ocel-isr-writer"}; !slices.Equal(gap.Stale, want) {
		t.Errorf("stale = %v, want %v", gap.Stale, want)
	}
	command := gap.RepairCommand(environmentv1.Tier_TIER_PRODUCTION)
	if want := "ocel bootstrap production --features cloudflare-edge"; command != want {
		t.Errorf("repair command = %q, want %q", command, want)
	}
	if features := requiredFeatures(status); !slices.Equal(features, []string{"cloudflare-edge"}) {
		t.Errorf("required features = %v, want the feature once", features)
	}
}
