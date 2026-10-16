package gcp

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAGCPBootstrapsPartsAreTheBaseItemsEachInstalledFeatureAndEachEdgePart(t *testing.T) {
	t.Parallel()

	b, _ := fronting(t)
	read := surveyed(albFeature)
	read.Region = "europe-west1"
	stacks := []provider.BootstrapStack{
		{Name: "acme-prod/production", Present: true, DigestCurrent: true},
		{Name: "acme-prod/production/" + albFeature, Feature: albFeature, Present: true, DigestCurrent: true},
		{Name: "acme-prod/production/" + networkFeature, Feature: networkFeature},
		{Name: "cloudflare/ocel-releases-store", Feature: albFeature, Present: true, DigestCurrent: true},
	}

	parts := b.surveyedParts(read, stacks)

	if len(parts) != 3 || parts[0].Name != provider.BasePart || parts[1].Name != albFeature || parts[2].Name != "cloudflare/ocel-releases-store" {
		t.Fatalf("parts = %+v, want the base, %s and the edge's part", parts, albFeature)
	}
	base := parts[0]
	if !base.Current {
		t.Errorf("base = %+v, want it current as its stack is", base)
	}
	if len(base.Resources) != len(bootstrapItems(read.Names, read.Tier, false)) {
		t.Errorf("base holds %d resources, want one per base item", len(base.Resources))
	}
	if base.Shape == nil || len(base.Shape.Resources) != len(base.Resources) || base.Shape.Region != "europe-west1" {
		t.Errorf("base shape = %+v, want every base item priced in the tier's region", base.Shape)
	}
	if alb := parts[1]; alb.Shape == nil || len(alb.Shape.Resources) == 0 || alb.Summary == "" {
		t.Errorf("%s = %+v, want the load balancer it stands up priced and summarised", albFeature, alb)
	}
	if edgePart := parts[2]; !edgePart.Current || edgePart.Shape != nil {
		t.Errorf("edge part = %+v, want it current and its idle cost unknown", edgePart)
	}
}
