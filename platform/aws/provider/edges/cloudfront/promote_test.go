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
	if err := stack.Promote(ctx, edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": first.Identity}}, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	w.dynamo.beforePut = func(key string, items map[string]map[string]ddbtypes.AttributeValue) {
		if strings.Contains(key, "pointers#") {
			items[key]["rev"] = &ddbtypes.AttributeValueMemberS{Value: "moved-by-another-deploy"}
		}
	}

	err := stack.Promote(ctx, edge.Promotion{PromotionID: "p2", Ts: 2, Builds: map[string]string{"web": second.Identity}}, "", edge.DiscardReporter())
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
