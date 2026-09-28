package apigateway

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestAFlipWhosePromotionIsNoLongerActiveCreatesNoAPIForItsPointer(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	_, stack := previewing(t, w)
	record := router.DeploymentRecord{App: "web", Build: "d1.f1", Entry: "/", EntryFunction: previewEntry}
	if err := openRouter(stack).Ledger.PutStaged(ctx, record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	displaced := errors.New("another promotion displaced this one")

	err := openRouter(stack).Flip(ctx, router.Flip{
		Pointer:     previewPoint,
		Promotion:   router.Promotion{PromotionID: "p-displaced", Ts: 1, Builds: map[string]string{"web": record.Build}},
		StillActive: func(context.Context) error { return displaced },
	}, progress.DiscardProgress())
	if !errors.Is(err, displaced) {
		t.Fatalf("Flip with a StillActive that refuses = %v, want that refusal", err)
	}
	w.gateway.mu.Lock()
	defer w.gateway.mu.Unlock()
	if api := w.gateway.named(previewAPIName); api != nil {
		t.Errorf("a refused flip left REST API %s behind; a flip whose promotion is no longer active moves and creates nothing", previewAPIName)
	}
}
