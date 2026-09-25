package apigateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestPromoteLeavesTheStageOnTheLedgersPromotionWhenItsPointerMovedUnderneath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	stack := reconciled(t, w)
	first := edge.DeploymentRecord{App: "web", Identity: "d1.f1", Entry: "/", EntryFunction: "conformance-prod-web-r1111aaaa", AssetPrefix: "assets/one"}
	second := edge.DeploymentRecord{App: "web", Identity: "d2.f2", Entry: "/", EntryFunction: "conformance-prod-web-r2222bbbb", AssetPrefix: "assets/two"}
	for _, record := range []edge.DeploymentRecord{first, second} {
		if err := stack.Ledger().PutStaged(ctx, record); err != nil {
			t.Fatalf("PutStaged: %v", err)
		}
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
	api := w.gateway.named(productionAPIName())
	if got := api.variables[entryVariable]; got != first.EntryFunction {
		t.Errorf("the stage serves %q, want %q: a promotion the ledger refused must not stay live", got, first.EntryFunction)
	}
	if got := api.variables[assetsVariable]; got != first.AssetPrefix {
		t.Errorf("the stage serves assets %q, want %q", got, first.AssetPrefix)
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
	staged(t, stack, entryFunction, "assets/")
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", Certificate: "arn:aws:acm:eu-west-1:123456789012:certificate/abc"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	w.gateway.deleteDomainErr = errors.New("the domain name is in use")

	if err := stack.Destroy(ctx); err == nil {
		t.Fatal("Destroy = nil, want the unbind failure surfaced")
	}
	if !ledgerRowsHeld(w) {
		t.Error("the deployments ledger was erased while shop.example.com is still bound; a re-run would not know to unbind it")
	}

	w.gateway.deleteDomainErr = nil
	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if ledgerRowsHeld(w) {
		t.Error("the ledger survived the re-run that unbound every hostname")
	}
}

func ledgerRowsHeld(w *world) bool {
	w.dynamo.mu.Lock()
	defer w.dynamo.mu.Unlock()
	for key := range w.dynamo.items {
		if strings.HasPrefix(key, "ledger#") {
			return true
		}
	}
	return false
}
