package conformance

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func RunContainerReleases(t *testing.T, hooks *provider.ContainerReleaseHooks) {
	t.Helper()

	if hooks.ScaleUp == nil || hooks.ScaleDown == nil {
		t.Fatal("ContainerReleases leaves ScaleUp or ScaleDown nil, and a release one promotion scales down is then never scaled up before another serves it")
	}
	release, err := provider.ParseRelease("0123456789abcdef0123456789abcdef~0123456789ab")
	if err != nil {
		t.Fatal(err)
	}
	untouched := []provider.AppRelease{{App: "web", Release: release}}
	ctx := context.Background()

	t.Run("scaling up releases no container runs changes nothing", func(t *testing.T) {
		promoted := provider.PromotedReleases{Slug: "conformance-untouched", Tier: environment.TierPreview, Env: "pr-0", Releases: untouched}
		if err := hooks.ScaleUp(ctx, promoted, progress.Discard()); err != nil {
			t.Errorf("ScaleUp() = %v, want a release that runs no container passed over", err)
		}
	})
	t.Run("scaling down releases no container runs changes nothing", func(t *testing.T) {
		demoted := provider.DemotedReleases{Slug: "conformance-untouched", Tier: environment.TierPreview, Env: "pr-0", Previous: untouched, Older: untouched}
		if err := hooks.ScaleDown(ctx, demoted, progress.Discard()); err != nil {
			t.Errorf("ScaleDown() = %v, want a release that runs no container passed over", err)
		}
	})
}
