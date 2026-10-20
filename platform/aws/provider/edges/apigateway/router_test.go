package apigateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestAPointerMoveWhosePromotionIsNoLongerActiveCreatesNoAPIForItsPointer(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	_, stack := previewing(t, w)
	record := router.ReleaseRecord{App: "web", Release: "d1.f1", RootFunction: "/", RootFunctionPhysical: previewEntry}
	if err := openRouter(stack).Ledger.PutStaged(ctx, record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	displaced := errors.New("another promotion displaced this one")

	err := openRouter(stack).MovePointer(ctx, router.PointerMove{
		Pointer:     previewPoint,
		Promotion:   router.Promotion{PromotionID: "p-displaced", Ts: 1, Releases: map[string]string{"web": record.Release}},
		StillActive: func(context.Context) error { return displaced },
	}, progress.Discard())
	if !errors.Is(err, displaced) {
		t.Fatalf("MovePointer with a StillActive that refuses = %v, want that refusal", err)
	}
	w.gateway.mu.Lock()
	defer w.gateway.mu.Unlock()
	if api := w.gateway.named(previewAPIName); api != nil {
		t.Errorf("a refused pointer move left REST API %s behind; a pointer move whose promotion is no longer active moves and creates nothing", previewAPIName)
	}
}

func promote(t *testing.T, stack edge.EdgeStack, record router.ReleaseRecord, promotion string, ts int64) {
	t.Helper()
	if err := openRouter(stack).Ledger.PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	move := router.PointerMove{Promotion: router.Promotion{PromotionID: promotion, Ts: ts, Releases: map[string]string{record.App: record.Release}}}
	if err := openRouter(stack).MovePointer(context.Background(), move, progress.Discard()); err != nil {
		t.Fatalf("MovePointer: %v", err)
	}
}

func TestPromoteRefusesAContainerReleaseByName(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	record := router.ReleaseRecord{App: "web", Release: "d1.f1", Image: "ocel/web:sha256-abc", Origin: "https://abc.eu-west-1.awsapprunner.com"}
	if err := openRouter(stack).Ledger.PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}

	promotion := router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": record.Release}}
	err := openRouter(stack).MovePointer(context.Background(), router.PointerMove{Promotion: promotion}, progress.Discard())
	if err == nil || !strings.Contains(err.Error(), "container") || !strings.Contains(err.Error(), "cloudfront") {
		t.Fatalf("Promote of a container release = %v, want it refused with the edge that can front one: this edge invokes a function, and a container has none", err)
	}
	if got := w.gateway.count("UpdateStage"); got != 0 {
		t.Errorf("UpdateStage calls = %d, want none: a refused promotion moves nothing", got)
	}
}

func TestPromoteMovesTheStageOnce(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	record := staged(t, stack, entryFunction, "shop/prod/web/r1234abcd")

	promotion := router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": record.Release}}
	if err := openRouter(stack).MovePointer(context.Background(), router.PointerMove{Promotion: promotion}, progress.Discard()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	if got := w.gateway.count("UpdateStage"); got != 1 {
		t.Errorf("UpdateStage calls = %d, want exactly one; promoting is moving one stage", got)
	}
	api := w.gateway.named(productionAPIName())
	if api.variables[entryVariable] != record.RootFunctionPhysical {
		t.Errorf("stage variable %s = %q, want the root function %q", entryVariable, api.variables[entryVariable], record.RootFunctionPhysical)
	}
	if want := "assets/shop/prod/web/r1234abcd/"; api.variables[assetsVariable] != want {
		t.Errorf("stage variable %s = %q, want %q: the release's assets prefix the static integrations read under", assetsVariable, api.variables[assetsVariable], want)
	}
	history, err := openRouter(stack).Ledger.History(context.Background(), "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 1 || history[0].PromotionID != "p1" || !history[0].Active {
		t.Errorf("history = %v, want the promotion just made, in effect", history)
	}
}

func TestPromoteServesTheFunctionTheDeployNamed(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	record := router.ReleaseRecord{
		App:                  "web",
		Release:              "d1.f1",
		RootFunction:         "/",
		RootFunctionPhysical: entryFunction,
	}
	if err := openRouter(stack).Ledger.PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}

	promotion := router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": record.Release}}
	if err := openRouter(stack).MovePointer(context.Background(), router.PointerMove{Promotion: promotion}, progress.Discard()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	api := w.gateway.named(productionAPIName())
	if api.variables[entryVariable] != entryFunction {
		t.Errorf("stage variable %s = %q, want the Lambda the deploy created, not the route it serves", entryVariable, api.variables[entryVariable])
	}
	if api.variables[assetsVariable] != unsetVariable {
		t.Errorf("stage variable %s = %q, want it cleared where the release built no static prefix; the previous release's objects must not survive the promotion", assetsVariable, api.variables[assetsVariable])
	}
}

func TestPromoteRefusesWhatTheStageCannotServe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("a release the ledger never staged", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)

		err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "d1.f1"}}}, progress.Discard())
		if err == nil {
			t.Fatal("Promote succeeded against a promotion nothing was staged for")
		}
		assertStageUnmoved(t, w)
	})

	t.Run("a record from before the root function was recorded", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		record := staged(t, stack, "", "shop/prod/web/r1111aaaa")

		err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": record.Release}}}, progress.Discard())
		if err == nil {
			t.Fatal("Promote succeeded on a record naming no root function")
		}
		if !strings.Contains(err.Error(), "root function") {
			t.Errorf("error = %v, want it to name what the record is missing", err)
		}
		assertUnserved(t, err)
		assertStageUnmoved(t, w)
	})

	t.Run("more apps than one stage can serve", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		for _, app := range []string{"api", "web"} {
			record := router.ReleaseRecord{App: app, Release: "d1.f1", RootFunction: "/", RootFunctionPhysical: "conformance-prod-" + app + "-r1234abcd"}
			if err := openRouter(stack).Ledger.PutStaged(ctx, record); err != nil {
				t.Fatalf("PutStaged(%s): %v", app, err)
			}
		}

		promotion := router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"api": "d1.f1", "web": "d1.f1"}}
		err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: promotion}, progress.Discard())
		if err == nil {
			t.Fatal("Promote succeeded on two apps, want the one-app limit surfaced rather than one app silently served")
		}
		for _, want := range []string{"api", "web", "cloudflare"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to name %q", err, want)
			}
		}
		assertUnserved(t, err)
		assertStageUnmoved(t, w)
	})
}

func assertStageUnmoved(t *testing.T, w *world) {
	t.Helper()
	if got := w.gateway.count("UpdateStage"); got != 0 {
		t.Errorf("UpdateStage calls = %d, want none behind a promotion the edge refused", got)
	}
	api := w.gateway.named(productionAPIName())
	if api.variables[entryVariable] != unsetVariable {
		t.Errorf("stage variable %s = %q, want the stage left where it was", entryVariable, api.variables[entryVariable])
	}
}

func assertUnserved(t *testing.T, err error) {
	t.Helper()
	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Errorf("MovePointer = %v, want router.Unserved: the stage never moved, so the ledger must take the promotion back", err)
	}
}

func TestAPointerMoveTheStageRefusesIsUnserved(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	record := staged(t, stack, entryFunction, "shop/prod/web/r1234abcd")
	w.gateway.stageErr = errors.New("stage is being updated by another operation")

	promotion := router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": record.Release}}
	err := openRouter(stack).MovePointer(context.Background(), router.PointerMove{Promotion: promotion}, progress.Discard())
	if err == nil {
		t.Fatal("Promote succeeded, want the stage failure surfaced")
	}
	assertUnserved(t, err)
}

func TestRollbackMovesTheStageOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	stack := reconciled(t, w)
	first := router.ReleaseRecord{App: "web", Release: "d1.f1", RootFunction: "/", RootFunctionPhysical: "conformance-prod-web-r1111aaaa", StorageCoordinate: "shop/prod/web/r1111aaaa"}
	second := router.ReleaseRecord{App: "web", Release: "d2.f2", RootFunction: "/", RootFunctionPhysical: "conformance-prod-web-r2222bbbb", StorageCoordinate: "shop/prod/web/r2222bbbb"}
	for _, record := range []router.ReleaseRecord{first, second} {
		if err := openRouter(stack).Ledger.PutStaged(ctx, record); err != nil {
			t.Fatalf("PutStaged: %v", err)
		}
	}
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": first.Release}}}, progress.Discard()); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p2", Ts: 2, Releases: map[string]string{"web": second.Release}}}, progress.Discard()); err != nil {
		t.Fatalf("Promote(p2): %v", err)
	}

	before := w.gateway.count("UpdateStage")
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p3", Ts: 3, Releases: map[string]string{"web": first.Release}}}, progress.Discard()); err != nil {
		t.Fatalf("rollback to p1's build: %v", err)
	}
	if got := w.gateway.count("UpdateStage") - before; got != 1 {
		t.Errorf("UpdateStage calls for the rollback = %d, want exactly one", got)
	}

	api := w.gateway.named(productionAPIName())
	if api.variables[entryVariable] != first.RootFunctionPhysical {
		t.Errorf("stage variable %s = %q, want the rolled-back release %q", entryVariable, api.variables[entryVariable], first.RootFunctionPhysical)
	}
	history, err := openRouter(stack).Ledger.History(ctx, "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 3 || history[0].PromotionID != "p3" || !history[0].Active || history[0].Releases["web"] != first.Release {
		t.Errorf("history = %v, want p3 in effect with p1's build over the two promotions before it", history)
	}
}

func TestARollbackTheStageRefusesIsUnserved(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	stack := reconciled(t, w)
	record := staged(t, stack, entryFunction, "shop/prod/web/r1111aaaa")
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": record.Release}}}, progress.Discard()); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	w.gateway.stageErr = errors.New("stage is being updated by another operation")

	err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p2", Ts: 2, Releases: map[string]string{"web": record.Release}}}, progress.Discard())
	if err == nil {
		t.Fatal("rollback succeeded, want the stage failure surfaced")
	}
	assertUnserved(t, err)
}
