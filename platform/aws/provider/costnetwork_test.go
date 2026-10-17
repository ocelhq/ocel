package aws_test

import (
	"context"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

func shapedWithUsage(t *testing.T, app string) *costv1.ResourceSet {
	t.Helper()
	client, _ := costServed(t)
	manifest := shopManifest()
	manifest.Usages = []*contractv1.ManifestUsage{{App: app, Resource: "main"}}
	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    manifest,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	return set
}

func TestAServerlessAppUsingPostgresPutsANATGatewayInTheTiersSharedCost(t *testing.T) {
	set := shapedWithUsage(t, "web")

	counts := typeCounts(set)
	if counts["aws_nat_gateway"] != 1 || counts["aws_eip"] != 1 {
		t.Fatalf("shaped %d NAT gateways and %d elastic IPs, want the one pair a serverless app that reaches the database makes the tier pay for", counts["aws_nat_gateway"], counts["aws_eip"])
	}
	for _, r := range set.GetResources() {
		if r.GetType() == "aws_nat_gateway" && r.GetScope() != "project:shop/shared:production" {
			t.Errorf("NAT gateway is in scope %q, want the tier's shared scope", r.GetScope())
		}
	}
}

func TestAContainerAppUsingPostgresPaysForNoNATGateway(t *testing.T) {
	set := shapedWithUsage(t, "api")

	if counts := typeCounts(set); counts["aws_nat_gateway"] != 0 || counts["aws_eip"] != 0 {
		t.Errorf("shaped %d NAT gateways and %d elastic IPs, want none: a container app runs in the VPC already", counts["aws_nat_gateway"], counts["aws_eip"])
	}
}
