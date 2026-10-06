package providerserver_test

import (
	"context"
	"slices"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestPreflightProbesPushAccessToTheProvidersOwnRegistryOnceItsBootstrapIsRead(t *testing.T) {
	client, vendor := hostingServed(t, ownRegistry, nil)
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	vendor.ImageStore().DenyPushes(refusal.Refuse(refusal.CodeDenied, "registry.invalid refuses robot a push"))

	resp, events, err := preflightWithSteps(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Containers:   []*contractv1.ContainerApp{{App: "web"}},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if problems := resp.GetCredentialProblems(); len(problems) != 1 || problems[0].GetProvider() != ownRegistry.Server {
		t.Errorf("credential problems = %v, want the provider's own registry refusing the push", problems)
	}
	if probed := vendor.ImageStore().Probed(); len(probed) != 1 || probed[0] != "web" {
		t.Errorf("push access probed for %q, want web", probed)
	}
	if steps := startedSteps(events); !slices.Contains(steps, "registry.invalid: Checking push access to registry.invalid/ocel/acme") {
		t.Errorf("steps = %q, want the push probe as a step of its own", steps)
	}
	for _, event := range events {
		if encodingContains(t, event, ownRegistry.Password) {
			t.Fatal("the preflight stream contains the provider's own registry password")
		}
	}
}

func TestPreflightResolvesNoOwnRegistryForAProjectThatNamesItsOwn(t *testing.T) {
	client, vendor := hostingServed(t, ownRegistry, nil)
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	if _, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier:    environmentv1.Tier_TIER_PRODUCTION,
		Containers:      []*contractv1.ContainerApp{{App: "web"}},
		ProjectRegistry: &contractv1.ImageRegistry{Server: "ghcr.io", Namespace: "acme", Username: "acme", Password: "hunter2"},
	}); err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if asked := vendor.asking(); len(asked) != 0 {
		t.Errorf("the provider's own registry was resolved for %v, want it left alone when the project names its own", asked)
	}
}
