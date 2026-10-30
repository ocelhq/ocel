package gcp

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
)

func TestAGCPBootstrapsPartsAreTheBaseItemsEachInstalledPartAndEachEdgePart(t *testing.T) {
	t.Parallel()

	b, _ := fronting(t)
	read := surveyed(bootstrap.CloudflareOriginPart)
	read.Region = "europe-west1"
	read.Stamp.Digest = "sha256:base"
	stacks := []provider.BootstrapStack{
		{Name: "acme-prod/production", Present: true, DigestCurrent: true},
		{Name: "acme-prod/production/" + bootstrap.CloudflareOriginPart, Part: bootstrap.CloudflareOriginPart, Present: true, DigestCurrent: true},
		{Name: "acme-prod/production/" + bootstrap.CloudflareOriginPart, Part: bootstrap.CloudflareOriginPart},
		{Name: "cloudflare/ocel-releases-store", Part: bootstrap.CloudflareOriginPart, Present: true, DigestCurrent: true},
	}

	parts := bootstrap.SurveyedParts(b, read, stacks)

	if len(parts) != 3 || parts[0].Name != provider.BasePart || parts[1].Name != bootstrap.CloudflareOriginPart || parts[2].Name != "cloudflare/ocel-releases-store" {
		t.Fatalf("parts = %+v, want the base, %s and the edge's part", parts, bootstrap.CloudflareOriginPart)
	}
	base := parts[0]
	if !base.Current || base.Digest != "sha256:base" {
		t.Errorf("base = %+v, want it current as its stack is, with the digest its stamp records", base)
	}
	if len(base.Resources) != len(bootstrap.Resources(read.Names, read.Tier, false)) {
		t.Errorf("base holds %d resources, want one per base item", len(base.Resources))
	}
	if base.Inventory == nil || len(base.Inventory.Resources) != len(base.Resources) || base.Inventory.Region != "europe-west1" {
		t.Errorf("base inventory = %+v, want every base item priced in the tier's region", base.Inventory)
	}
	if origin := parts[1]; origin.Inventory != nil || origin.Summary == "" {
		t.Errorf("%s = %+v, want it summarised and unpriced: it holds secrets and an asset reader, and no load balancer", bootstrap.CloudflareOriginPart, origin)
	}
	if edgePart := parts[2]; !edgePart.Current || edgePart.Inventory != nil {
		t.Errorf("edge part = %+v, want it current and its idle cost unknown", edgePart)
	}
}
