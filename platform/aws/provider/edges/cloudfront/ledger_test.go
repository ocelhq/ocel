package cloudfront

import (
	"context"
	"errors"
	"strings"
	"testing"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestPromoteLeavesTheRouteOnTheLedgersPromotionWhenItsPointerMovedUnderneath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	stack := reconciled(t, w)
	bound(t, stack)
	first := staged(t, stack, fakeEntryURL, "assets/one")
	second := edge.DeploymentRecord{App: "web", Identity: "d2.f2", Entry: "/", EntryFunction: entryFunction, FunctionURLs: map[string]string{"/": fakeEntryURL}, AssetPrefix: "assets/two"}
	if err := stack.Ledger().PutStaged(ctx, second); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	if err := stack.Promote(ctx, edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": first.Identity}}, "", edge.DiscardProgress()); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	w.dynamo.beforePut = func(key string, items map[string]map[string]ddbtypes.AttributeValue) {
		if strings.Contains(key, "pointers#") {
			items[key]["rev"] = &ddbtypes.AttributeValueMemberS{Value: "moved-by-another-deploy"}
		}
	}

	err := stack.Promote(ctx, edge.Promotion{PromotionID: "p2", Ts: 2, Builds: map[string]string{"web": second.Identity}}, "", edge.DiscardProgress())
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeBusy {
		t.Fatalf("Promote(p2) = %v, want the busy refusal a moved pointer earns", err)
	}
	if got := routeOn(t, w, stack, boundHost); got.Release != first.Identity {
		t.Errorf("%s routes to release %q, want %q: a promotion the ledger refused must not stay live", boundHost, got.Release, first.Identity)
	}
	history, err := stack.Ledger().History(ctx, "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	for _, entry := range history {
		if entry.Active && entry.PromotionID != "p1" {
			t.Errorf("history points at %s, want p1", entry.PromotionID)
		}
	}
}

func TestDestroyKeepsTheLedgerWhileAHostnameIsStillBound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	stack := reconciled(t, w)
	bound(t, stack)
	staged(t, stack, fakeEntryURL, fakeAssetPrefix)
	w.front.aliasErr = errors.New("the distribution is being updated")

	if err := stack.Destroy(ctx); err == nil {
		t.Fatal("Destroy = nil, want the unbind failure surfaced")
	}
	if len(w.dynamo.items) == 0 {
		t.Error("the deployments ledger was erased while the hostname is still bound; a re-run would not know to unbind it")
	}

	w.front.aliasErr = nil
	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if len(w.dynamo.items) != 0 {
		t.Errorf("the ledger left %d items behind after the re-run, want none", len(w.dynamo.items))
	}
}
