package aws_test

import (
	"context"
	"testing"

	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func inventoryWithUsage(t *testing.T, app string) *costv1.ResourceSet {
	t.Helper()
	client := inventoryServed(t)
	manifest := shopManifest()
	manifest.Usages = []*contractv1.ManifestUsage{{App: app, Resource: "main"}}
	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    manifest,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	return set
}

func TestAServerlessAppUsingPostgresListsANATGatewayInTheTiersSharedScope(t *testing.T) {
	set := inventoryWithUsage(t, "web")

	counts := typeCounts(set)
	if counts["aws_nat_gateway"] != 2 || counts["aws_eip"] != 2 {
		t.Fatalf("listed %d NAT gateways and %d elastic IPs, want the pair per zone a serverless app that reaches the database makes the production tier pay for", counts["aws_nat_gateway"], counts["aws_eip"])
	}
	for _, r := range set.GetResources() {
		if r.GetType() == "aws_nat_gateway" && r.GetScope() != "project:shop/shared:production" {
			t.Errorf("NAT gateway is in scope %q, want the tier's shared scope", r.GetScope())
		}
	}
}

func TestAContainerAppUsingPostgresListsNoNATGateway(t *testing.T) {
	set := inventoryWithUsage(t, "api")

	if counts := typeCounts(set); counts["aws_nat_gateway"] != 0 || counts["aws_eip"] != 0 {
		t.Errorf("listed %d NAT gateways and %d elastic IPs, want none: a container app runs in the VPC already", counts["aws_nat_gateway"], counts["aws_eip"])
	}
}

func TestADeployThatNeedsTheTiersNetworkListsItInTheTiersSharedScope(t *testing.T) {
	set := inventoryWithUsage(t, "api")

	counts := typeCounts(set)
	if counts["aws_vpc"] != 1 || counts["aws_subnet"] != 6 || counts["aws_internet_gateway"] != 1 || counts["aws_vpc_endpoint"] != 2 {
		t.Fatalf("listed %v, want one VPC, six subnets, one internet gateway and two gateway endpoints for the network the database and container app run in", counts)
	}
	for _, r := range set.GetResources() {
		if r.GetType() == "aws_vpc" && r.GetScope() != "project:shop/shared:production" {
			t.Errorf("the VPC is in scope %q, want the tier's shared scope", r.GetScope())
		}
	}
}
