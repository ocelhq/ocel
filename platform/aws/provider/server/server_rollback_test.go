package server

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestPromotionCarriesTheRecordedFlipBound(t *testing.T) {
	for _, kind := range []edge.Kind{edge.KindCloudflare, edge.KindNative, edge.KindNone} {
		bound := edge.CapabilitiesOf(kind).FlipBound()
		got := toPromotionProto(edge.Promotion{PromotionID: "promo-1", Flip: &bound}).GetFlipBound()
		if got == nil {
			t.Fatalf("%s promotion carries no flip bound, want the edge's declared %v", kind, bound)
		}
		if got.GetTypicalMs() != bound.Typical.Milliseconds() || got.GetPublished() != bound.Published {
			t.Errorf("%s flip bound = %v, want %v", kind, got, bound)
		}
	}

	t.Run("history carries it too", func(t *testing.T) {
		bound := edge.FlipBound{Typical: 5 * time.Second}
		history := toPromotionHistory([]edge.HistoryEntry{
			{Promotion: edge.Promotion{PromotionID: "promo-1", Flip: &bound}, Active: true},
		})
		if len(history) != 1 {
			t.Fatalf("history = %d entries, want 1", len(history))
		}
		if got := history[0].GetPromotion().GetFlipBound(); got.GetTypicalMs() != 5000 || got.GetPublished() {
			t.Errorf("history flip bound = %v, want 5000 ms unpublished", got)
		}
	})

	t.Run("an unrecorded bound stays absent", func(t *testing.T) {
		if got := toPromotionProto(edge.Promotion{PromotionID: "promo-1"}).GetFlipBound(); got != nil {
			t.Errorf("flip bound = %v, want none for a promotion that recorded none", got)
		}
	})

	t.Run("the deploy result reports it", func(t *testing.T) {
		bound := edge.FlipBound{Typical: 5 * time.Second, Published: true}
		got := deployedResult(deploy.Result{PromotionID: "promo-1", Flip: &bound}).GetResult().GetFlipBound()
		if got.GetTypicalMs() != 5000 || !got.GetPublished() {
			t.Errorf("result flip bound = %v, want 5000 ms published", got)
		}
	})
}
