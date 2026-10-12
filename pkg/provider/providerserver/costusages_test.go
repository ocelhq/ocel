package providerserver_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type shapeRecording struct {
	provider.Provider
	mu       sync.Mutex
	requests []provider.ShapeRequest
}

func (s *shapeRecording) Hooks() provider.Hooks {
	hooks := s.Provider.Hooks()
	inner := hooks.Cost.Shape
	hooks.Cost = &provider.CostHooks{
		Estimate: hooks.Cost.Estimate,
		Shape: func(ctx context.Context, req provider.ShapeRequest) (*costv1.ResourceSet, error) {
			s.mu.Lock()
			s.requests = append(s.requests, req)
			s.mu.Unlock()
			return inner(ctx, req)
		},
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

func TestShapeOfAnEphemeralPreviewNamesWhatEachAppUses(t *testing.T) {
	recording := &shapeRecording{Provider: fake.NewProvider(fake.Options{Region: "nowhere"})}
	client, _ := costServed(t, recording)
	req := shapeRequest()
	req.Environment = &environmentv1.Environment{
		Tier:      environmentv1.Tier_TIER_PREVIEW,
		Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
		Identity:  "preview",
	}

	if _, err := client.Shape(context.Background(), req); err != nil {
		t.Fatalf("Shape() error = %v", err)
	}

	if got := usedBy(recording.requests[0].Usages, "web"); len(got) == 0 {
		t.Errorf("web uses %v, want the resources the preview's own infra stack provisions", got)
	}
}

func TestShapeHandsTheProviderWhichResourcesEachAppUses(t *testing.T) {
	recording := &shapeRecording{Provider: fake.NewProvider(fake.Options{Region: "nowhere"})}
	client, _ := costServed(t, recording)
	req := shapeRequest()
	req.Manifest = twoAppRequest().GetManifest()

	if _, err := client.Shape(context.Background(), req); err != nil {
		t.Fatalf("Shape() error = %v", err)
	}

	if len(recording.requests) != 1 {
		t.Fatalf("the provider was asked to shape %d times, want once", len(recording.requests))
	}
	usages := recording.requests[0].Usages
	if got, want := usedBy(usages, "admin"), []string{"orders", "uploads"}; !slices.Equal(got, want) {
		t.Errorf("admin uses %v, want %v: the usage edges the manifest names", got, want)
	}
	if got, want := usedBy(usages, "web"), []string{"orders"}; !slices.Equal(got, want) {
		t.Errorf("web uses %v, want %v", got, want)
	}
}
