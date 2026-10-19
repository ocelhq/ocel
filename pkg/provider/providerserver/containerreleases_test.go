package providerserver_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
)

type containerScaling struct {
	mu         sync.Mutex
	scaledUp   []provider.PromotedReleases
	scaledDown []provider.DemotedReleases
	upErr      error
	downErr    error
}

func (c *containerScaling) installOn(vendor *fake.Provider) *containerScaling {
	vendor.WithHooks(func(hooks *provider.Hooks) {
		hooks.ContainerReleases = &provider.ContainerReleaseHooks{
			ScaleUp: func(_ context.Context, promoted provider.PromotedReleases, _ progress.Log) error {
				c.mu.Lock()
				defer c.mu.Unlock()
				c.scaledUp = append(c.scaledUp, promoted)
				return c.upErr
			},
			ScaleDown: func(_ context.Context, demoted provider.DemotedReleases, _ progress.Log) error {
				c.mu.Lock()
				defer c.mu.Unlock()
				c.scaledDown = append(c.scaledDown, demoted)
				return c.downErr
			},
		}
	})
	return c
}

func (c *containerScaling) readScaledUp() []provider.PromotedReleases {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.scaledUp)
}

func (c *containerScaling) readScaledDown() []provider.DemotedReleases {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.scaledDown)
}

func webRelease(t *testing.T, rendered string) provider.AppRelease {
	t.Helper()
	release, err := provider.ParseRelease(rendered)
	if err != nil {
		t.Fatal(err)
	}
	return provider.AppRelease{App: "web", Release: release}
}

func TestARollbackScalesUpTheReleasesItPromotesBeforeItMovesThePointer(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	seedPromotions(t, vendor, environment.TierProduction, "shop", "", "p1", "p2")
	scaling := (&containerScaling{}).installOn(vendor)
	var beforeMove []provider.PromotedReleases
	relayPlane(vendor).BeforeNextPointerMove(func() { beforeMove = scaling.readScaledUp() })

	if _, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	want := provider.PromotedReleases{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Releases: []provider.AppRelease{webRelease(t, releaseFor(0))}}
	if len(beforeMove) != 1 || !samePromoted(beforeMove[0], want) {
		t.Errorf("before the pointer moved the releases scaled up were %+v, want %+v, so the pointer never names a release with no task to answer", beforeMove, want)
	}
}

func TestARollbackWhoseReleasesCannotBeScaledUpLeavesThePointerOnThePromotionItServed(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	seedPromotions(t, vendor, environment.TierProduction, "shop", "", "p1", "p2")
	(&containerScaling{upErr: errors.New("web's release never turned healthy behind its target group")}).installOn(vendor)

	_, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"})

	if err == nil || !strings.Contains(err.Error(), "never turned healthy") {
		t.Fatalf("Rollback() = %v, want it refused with why the release did not scale up", err)
	}
	active, err := ledger.New(vendor.KeyValues(), environment.TierProduction, "shop").ActivePromotionID(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if active != "p2" {
		t.Errorf("the pointer names %q after the release failed to scale up, want p2 still serving", active)
	}
}

func TestARollbackScalesDownTheReleaseItReplacedAfterTheWindowAndEveryOlderReleaseNow(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	seedPromotions(t, vendor, environment.TierProduction, "shop", "", "p1", "p2", "p3")
	scaling := (&containerScaling{}).installOn(vendor)

	if _, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	want := provider.DemotedReleases{
		Slug:     "shop",
		Tier:     environment.TierProduction,
		Env:      "prod",
		Previous: []provider.AppRelease{webRelease(t, releaseFor(2))},
		Older:    []provider.AppRelease{webRelease(t, releaseFor(0))},
	}
	got := scaling.readScaledDown()
	if len(got) != 1 || !sameDemoted(got[0], want) {
		t.Errorf("the rollback scaled down %+v, want %+v: p3's release kept for the window, p1's scaled down now, and p2's, which serves, untouched", got, want)
	}
}

func TestAPromotionWhoseScaleDownFailsStillServesAndWarnsOfTheReleasesLeftRunning(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	seedPromotions(t, vendor, environment.TierProduction, "shop", "", "p1", "p2")
	(&containerScaling{downErr: errors.New("throttled")}).installOn(vendor)

	rolled, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("Rollback() error = %v, want the promotion to serve though scaling down failed", err)
	}

	warned := strings.Join(rolled.warnings(), "\n")
	if !strings.Contains(warned, "throttled") || !strings.Contains(warned, "the next promotion retries") {
		t.Errorf("warnings = %q, want one naming the failure and that the next promotion retries it", warned)
	}
}

func TestADeployScalesUpNothingAndScalesDownTheReleaseItReplacedOnceItsOwnServes(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	scaling := (&containerScaling{}).installOn(vendor)

	first, _ := deploy(t, client, deployRequest())
	second, _ := deploy(t, client, deployRequest())
	if !first.GetSuccess() || !second.GetSuccess() {
		t.Fatalf("deploys = %q, %q, want both to land", first.GetError(), second.GetError())
	}

	firstRelease := webRelease(t, first.GetApps()[0].GetRelease())
	if up := scaling.readScaledUp(); len(up) != 0 {
		t.Errorf("the deploys scaled up %+v, want nothing: every release a deploy promotes is new, so none was ever scaled down", up)
	}
	down := scaling.readScaledDown()
	if len(down) != 1 || !slices.Equal(down[0].Previous, []provider.AppRelease{firstRelease}) || len(down[0].Older) != 0 {
		t.Errorf("the deploys scaled down %+v, want the first replacing nothing and the second keeping the first's release for the window and nothing older", down)
	}
}

func samePromoted(got, want provider.PromotedReleases) bool {
	return got.Slug == want.Slug && got.Tier == want.Tier && got.Env == want.Env && slices.Equal(got.Releases, want.Releases)
}

func sameDemoted(got, want provider.DemotedReleases) bool {
	return got.Slug == want.Slug && got.Tier == want.Tier && got.Env == want.Env &&
		slices.Equal(got.Previous, want.Previous) && slices.Equal(got.Older, want.Older)
}
