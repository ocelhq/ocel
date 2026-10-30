package providerserver_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"

	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type inventoryRecording struct {
	provider.Provider
	mu       sync.Mutex
	requests []provider.InventoryRequest
}

func (s *inventoryRecording) Hooks() provider.Hooks {
	hooks := s.Provider.Hooks()
	inner := hooks.Inventory
	hooks.Inventory = func(ctx context.Context, req provider.InventoryRequest) (*costv1.ResourceSet, error) {
		s.mu.Lock()
		s.requests = append(s.requests, req)
		s.mu.Unlock()
		return inner(ctx, req)
	}
	return hooks
}

func usedBy(usages []provider.AppUsage, app string) []string {
	var names []string
	for _, usage := range usages {
		if usage.App != app {
			continue
		}
		for _, resource := range usage.Resources {
			names = append(names, resource.Name)
		}
	}
	slices.Sort(names)
	return names
}

func TestInventoryOfAnEphemeralPreviewNamesWhatEachAppUses(t *testing.T) {
	recording := &inventoryRecording{Provider: fake.NewProvider(fake.Options{Region: "nowhere"})}
	client := inventoryServed(t, recording)
	req := inventoryRequest()
	req.Environment = &environmentv1.Environment{
		Tier:      environmentv1.Tier_TIER_PREVIEW,
		Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
		Identity:  "preview",
	}

	if _, err := client.Inventory(context.Background(), req); err != nil {
		t.Fatalf("Inventory() error = %v", err)
	}

	if got := usedBy(recording.requests[0].Usages, "web"); len(got) == 0 {
		t.Errorf("web uses %v, want the resources the preview's own infra stack provisions", got)
	}
}

func TestInventoryHandsTheProviderWhichResourcesEachAppUses(t *testing.T) {
	recording := &inventoryRecording{Provider: fake.NewProvider(fake.Options{Region: "nowhere"})}
	client := inventoryServed(t, recording)
	req := inventoryRequest()
	req.Manifest = twoAppRequest().GetManifest()

	if _, err := client.Inventory(context.Background(), req); err != nil {
		t.Fatalf("Inventory() error = %v", err)
	}

	if len(recording.requests) != 1 {
		t.Fatalf("the provider was asked for inventory %d times, want once", len(recording.requests))
	}
	usages := recording.requests[0].Usages
	if got, want := usedBy(usages, "admin"), []string{"orders", "uploads"}; !slices.Equal(got, want) {
		t.Errorf("admin uses %v, want %v: the usage edges the manifest names", got, want)
	}
	if got, want := usedBy(usages, "web"), []string{"orders"}; !slices.Equal(got, want) {
		t.Errorf("web uses %v, want %v", got, want)
	}
}
