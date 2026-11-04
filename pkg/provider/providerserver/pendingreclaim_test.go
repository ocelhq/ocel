package providerserver_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
)

func TestARollbackWhoseReclaimNeedsARunnerLeavesItPendingForTheNextPrune(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	releases := seedKept(t, vendor)
	vendor.FakeStacks().RefuseNextDestroy(provider.RefuseNeedsRunner("destroying a stack"))

	rolled, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("Rollback() whose reclaim needs a runner = %v, want it to succeed: its promotion serves", err)
	}
	if warned := rolled.warnings(); len(warned) > 0 {
		t.Errorf("Rollback() warned %q, want the reclaim left pending, not warned of", warned)
	}
	if said := strings.Join(said(rolled.events, progressv1.Level_LEVEL_INFO), "\n"); !strings.Contains(said, "ocel promotions prune") {
		t.Errorf("Rollback() said %q, want it to name what reclaims the dropped releases", said)
	}
	pointer, err := releases.Read(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pointer.PendingReclaims) != 1 || pointer.PendingReclaims[0].PromotionID != "p00" {
		t.Fatalf("pending reclaims = %+v, want p00, the promotion the rollback dropped", pointer.PendingReclaims)
	}
	if _, found, err := releases.Record(context.Background(), "web", releaseFor(0)); err != nil || !found {
		t.Fatalf("the record of p00's build = found %v, %v, want it kept until something reclaims it", found, err)
	}

	stream, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug:        "shop",
		KeepN:       20,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveStalePromotions() = %q, %v", result.GetError(), err)
	}
	if _, found, err := releases.Record(context.Background(), "web", releaseFor(0)); err != nil || found {
		t.Errorf("the record of p00's build after the prune = found %v, %v, want it reclaimed", found, err)
	}
	if pointer, err := releases.Read(context.Background(), ""); err != nil || len(pointer.PendingReclaims) > 0 {
		t.Errorf("pending reclaims after the prune = %+v, %v, want none", pointer.PendingReclaims, err)
	}
}

func pruneShop(client contractv1connect.ProviderServiceClient) (*progressv1.OperationResult, error) {
	stream, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug:        "shop",
		KeepN:       20,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		return nil, err
	}
	return drain(stream)
}

func TestAPruneWhoseReclaimNeedsARunnerKeepsThePendingReclaimsItTookUp(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	releases := seedKept(t, vendor)
	vendor.FakeStacks().RefuseNextDestroy(provider.RefuseNeedsRunner("destroying a stack"))
	if _, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatal(err)
	}

	vendor.FakeStacks().RefuseNextDestroy(provider.RefuseNeedsRunner("destroying a stack"))
	result, err := pruneShop(client)
	if err == nil && result.GetSuccess() {
		t.Fatal("RemoveStalePromotions() whose destroy needs a runner succeeded, want it refused")
	}
	pointer, err := releases.Read(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pointer.PendingReclaims) != 1 || pointer.PendingReclaims[0].PromotionID != "p00" {
		t.Fatalf("pending reclaims after the refused prune = %+v, want p00 still pending", pointer.PendingReclaims)
	}
	if _, found, err := releases.Record(context.Background(), "web", releaseFor(0)); err != nil || !found {
		t.Fatalf("the record of p00's build = found %v, %v, want it still tracked", found, err)
	}

	if result, err := pruneShop(client); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveStalePromotions() with an engine = %q, %v", result.GetError(), err)
	}
	if _, found, err := releases.Record(context.Background(), "web", releaseFor(0)); err != nil || found {
		t.Errorf("the record of p00's build after a prune that could destroy = found %v, %v, want it reclaimed", found, err)
	}
}

func removePR7(client contractv1connect.ProviderServiceClient) (*progressv1.OperationResult, error) {
	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug: "shop", Environment: &environmentv1.Environment{
			Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7", Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
		},
	})
	if err != nil {
		return nil, err
	}
	return drain(stream)
}

func TestAPreviewRemovalWhoseReclaimFailsLeavesItsReleasesPendingForTheNextRemoval(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	deployed, _ := deploy(t, client, previewRequest())
	if !deployed.GetSuccess() {
		t.Fatalf("Deploy() = %q", deployed.GetError())
	}
	release := deployed.GetApps()[0].GetRelease()
	releases := ledger.New(vendor.KeyValues(), environment.TierPreview, "shop")

	vendor.FakeStacks().RefuseNextDestroy(provider.RefuseNeedsRunner("destroying a stack"))
	if result, err := removePR7(client); err == nil && result.GetSuccess() {
		t.Fatal("RemoveEnvironment() whose destroy needs a runner succeeded, want it refused")
	}
	pointer, err := releases.Read(context.Background(), "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if len(pointer.PendingReclaims) != 1 || pointer.PendingReclaims[0].PromotionID != deployed.GetPromotionId() {
		t.Fatalf("pr-7's pending reclaims after the refused removal = %+v, want the promotion it dropped", pointer.PendingReclaims)
	}
	if _, found, err := releases.Record(context.Background(), "web", release); err != nil || !found {
		t.Fatalf("the record of pr-7's release = found %v, %v, want it still tracked", found, err)
	}

	if result, err := removePR7(client); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() again = %q, %v", result.GetError(), err)
	}
	if _, found, err := releases.Record(context.Background(), "web", release); err != nil || found {
		t.Errorf("the record of pr-7's release after a removal that could destroy = found %v, %v, want it reclaimed", found, err)
	}
	if names, err := releases.Pointers(context.Background()); err != nil || len(names) != 0 {
		t.Errorf("pointers after the removal = %v, %v, want none left", names, err)
	}
}
