package providerserver_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func listRecorded(t *testing.T, store records.Store, in records.Partition) []string {
	t.Helper()
	entries, err := store.List(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key.String())
	}
	return keys
}

func removedAfterADeploy(t *testing.T) *fake.Provider {
	t.Helper()
	client, vendor := deployedProject(t)
	if err := stackrecords.RecordPartsUsed(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop", []string{fake.PartCache}); err != nil {
		t.Fatal(err)
	}
	if result := removeProject(t, client, projectRequest()); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the project removed", result.GetError())
	}
	return vendor
}

func TestRemovingAProjectLeavesNoRecordOfIt(t *testing.T) {
	vendor := removedAfterADeploy(t)
	ctx := context.Background()
	store := vendor.KeyValues()
	tier := environment.TierProduction

	for _, key := range []records.Key{stackrecords.ProjectKey(tier, "shop"), stackrecords.EdgeStackKey(tier, "shop")} {
		recorded, err := records.ReadOrEmpty(ctx, store, key)
		if err != nil {
			t.Fatal(err)
		}
		if recorded.Revision != "" {
			t.Errorf("%s is still recorded after the project was removed", key)
		}
	}
	for _, partition := range []records.Partition{
		stackrecords.StacksPartition(tier, "shop"),
		stackrecords.EnvironmentsPartition(tier, "shop"),
	} {
		if left := listRecorded(t, store, partition); len(left) > 0 {
			t.Errorf("%s still holds %v after the project was removed", partition, left)
		}
	}
	wants, err := stackrecords.ListBootstrapWants(ctx, store, tier)
	if err != nil {
		t.Fatal(err)
	}
	for part, projects := range wants {
		if slices.Contains(projects, "shop") {
			t.Errorf("shop still wants bootstrap part %s after it was removed", part)
		}
	}
}

func TestRemovingAProjectForgetsThePartsItUsed(t *testing.T) {
	vendor := removedAfterADeploy(t)

	recorded, err := records.ReadOrEmpty(context.Background(), vendor.KeyValues(), stackrecords.PartsUsedKey(environment.TierProduction, "shop"))
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Revision != "" {
		t.Errorf("the parts shop used are still recorded after it was removed: %s", recorded.Value)
	}
}

func TestRemovingEveryPreviewOfAProjectForgetsItsPreviewLedger(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	if result, _ := deploy(t, client, previewRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	partition := ledger.Partition(environment.TierPreview, "shop")
	if len(listRecorded(t, vendor.KeyValues(), partition)) == 0 {
		t.Fatal("the preview deploy recorded nothing in its ledger, and a removal that forgets nothing proves nothing")
	}

	if result := removeProject(t, client, &contractv1.ProjectRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW},
	}); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q", result.GetError())
	}

	if left := listRecorded(t, vendor.KeyValues(), partition); len(left) > 0 {
		t.Errorf("the preview ledger of shop still holds %v after every preview was removed", left)
	}
}

func TestRemovingAProjectForgetsItsLedger(t *testing.T) {
	vendor := removedAfterADeploy(t)

	if left := listRecorded(t, vendor.KeyValues(), ledger.Partition(environment.TierProduction, "shop")); len(left) > 0 {
		t.Errorf("the ledger of shop still holds %v after it was removed", left)
	}
}
