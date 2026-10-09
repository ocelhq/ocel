package providerserver_test

import (
	"context"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func preflightProduction(t *testing.T, client contractv1connect.ProviderServiceClient) {
	t.Helper()
	if _, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
	}); err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
}

func TestAPreflightAndTheDeployAfterItDescribeTheBootstrapOnce(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	before := vendor.FakeBootstrap().Described()

	preflightProduction(t, client)
	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if got := vendor.FakeBootstrap().Described() - before; got != 1 {
		t.Errorf("the bootstrap was described %d times across a preflight and a deploy, want once", got)
	}
}

func TestADeployReadsTheBootstrapAfreshOnceABootstrapRanSinceThePreflight(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	preflightProduction(t, client)
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache, fake.FeatureImages},
	})
	before := vendor.FakeBootstrap().Described()
	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if got := vendor.FakeBootstrap().Described() - before; got != 1 {
		t.Errorf("the deploy after a bootstrap described the bootstrap %d times, want it read once, afresh", got)
	}
}
