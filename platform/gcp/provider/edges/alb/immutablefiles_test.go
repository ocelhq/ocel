package alb

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	objectsOfB1 = "/assets/shop/prod/web/r000000b1/"
	objectsOfB2 = "/assets/shop/prod/web/r000000b2/"
)

func servingB1ThenStaging(t *testing.T) (*world, func(id, build string) error) {
	t.Helper()
	_, w, stack := reconciled(t)
	stage(t, stack, staticRecord("b1"))
	stage(t, stack, staticRecord("b2"))
	promote(t, stack, "p1", "b1")
	bindShop(t, stack)
	promoting := openRouter(stack)
	return w, func(id, build string) error {
		return promoting.MovePointer(context.Background(), router.PointerMove{
			Promotion: router.Promotion{PromotionID: id, Releases: map[string]string{"web": build}},
		}, progress.Discard())
	}
}

func TestAPromotionGivesTheLiveAndTheNextReleaseEachOthersImmutableFilesBeforeItMovesTraffic(t *testing.T) {
	t.Parallel()

	w, move := servingB1ThenStaging(t)
	pins := w.pinCount()
	if err := move("p2", "b2"); err != nil {
		t.Fatalf("MovePointer(p2) = %v", err)
	}

	want := []immutableCopy{
		{From: objectsOfB1, To: objectsOfB2, PinsBefore: pins, Routed: objectsOfB1},
		{From: objectsOfB2, To: objectsOfB1, PinsBefore: pins, Routed: objectsOfB1},
	}
	if got := w.copied(); !reflect.DeepEqual(got, want) {
		t.Errorf("the promotion copied %+v, want %+v: until both the url map and the traffic pin have moved, "+
			"a page of either release may be answered with its chunks looked up under either release's prefix", got, want)
	}
}

func TestADeploymentPointerMoveCopiesNoFilesForTheHostnamesItLeavesAlone(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	stage(t, stack, staticRecord("b1"))
	stage(t, stack, staticRecord("b2"))
	promote(t, stack, "p1", "b1")
	bindShop(t, stack)
	if err := openRouter(stack).MovePointer(context.Background(), router.PointerMove{
		Pointer:   router.FormatDeploymentPointer("pr-42", "p2"),
		Promotion: router.Promotion{PromotionID: "p2", Releases: map[string]string{"web": "b2"}},
	}, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(deployment) = %v", err)
	}

	if got := w.copied(); len(got) != 0 {
		t.Errorf("the deployment's move copied %+v, want nothing: it moves no rewrite of the hostnames the default pointer serves", got)
	}
}

func TestARollbackGivesTheReleasesEachOthersImmutableFilesToo(t *testing.T) {
	t.Parallel()

	w, move := servingB1ThenStaging(t)
	if err := move("p2", "b2"); err != nil {
		t.Fatalf("MovePointer(p2) = %v", err)
	}
	if err := move("p3", "b1"); err != nil {
		t.Fatalf("MovePointer(p3) = %v", err)
	}

	got := w.copied()[2:]
	want := []immutableCopy{
		{From: objectsOfB2, To: objectsOfB1, PinsBefore: got[0].PinsBefore, Routed: objectsOfB2},
		{From: objectsOfB1, To: objectsOfB2, PinsBefore: got[0].PinsBefore, Routed: objectsOfB2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the rollback copied %+v, want %+v", got, want)
	}
}

func TestAPromotionWhoseImmutableFilesCannotBeCopiedMovesNothing(t *testing.T) {
	t.Parallel()

	w, move := servingB1ThenStaging(t)
	refused := errors.New("the bucket refused the copy")
	w.failCopies(refused)
	pins := w.pinCount()

	err := move("p2", "b2")
	var unserved router.Unserved
	if !errors.Is(err, refused) || !errors.As(err, &unserved) {
		t.Fatalf("MovePointer(p2) = %v, want %v reported as unserved", err, refused)
	}
	if got := w.pinCount(); got != pins {
		t.Errorf("the traffic pin moved %d times, want none: the new release's pages could reference chunks the live prefix lacks", got-pins)
	}
	expectStaticRoutes(t, w, "shop.example.com", staticRoutesFor("b1"), "a promotion that moved nothing leaves the rewrite on the live release")
}

func TestAPromotionThatMovedOneHostnameBeforeAnotherFailedSharesFilesFromWhereEachIsRouted(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	for _, build := range []string{"b1", "b2", "b3"} {
		stage(t, stack, staticRecord(build))
	}
	promote(t, stack, "p1", "b1")
	bindShop(t, stack)
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "www.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain(www) = %v", err)
	}
	w.failRoute("www.example.com", errors.New("the url map write was refused"))
	promoting := openRouter(stack)
	if err := promoting.MovePointer(context.Background(), router.PointerMove{
		Promotion: router.Promotion{PromotionID: "p2", Releases: map[string]string{"web": "b2"}},
	}, progress.Discard()); err == nil {
		t.Fatal("MovePointer(p2) = nil, want the refusal of www's write")
	}
	w.failRoute("www.example.com", nil)
	before := len(w.copied())
	promote(t, stack, "p3", "b3")

	objectsOfB3 := "/assets/shop/prod/web/r000000b3/"
	var froms []string
	for _, copied := range w.copied()[before:] {
		if copied.To == objectsOfB3 {
			froms = append(froms, copied.From)
		}
	}
	if want := []string{objectsOfB2, objectsOfB1}; !reflect.DeepEqual(froms, want) {
		t.Errorf("b3 was given the files of %v, want %v: shop was already rewritten onto b2 and www was still on b1", froms, want)
	}
}

func TestAUrlMapThatFailedToMoveIsMovedByTheNextPromotion(t *testing.T) {
	t.Parallel()

	w, move := servingB1ThenStaging(t)
	w.failRoute("shop.example.com", errors.New("the url map write was refused"))
	if err := move("p2", "b2"); err == nil {
		t.Fatal("MovePointer(p2) = nil, want the url map's refusal")
	}
	expectStaticRoutes(t, w, "shop.example.com", staticRoutesFor("b1"), "the refused write left the rewrite on b1, whose prefix now holds b2's files too")

	w.failRoute("shop.example.com", nil)
	if err := move("p3", "b2"); err != nil {
		t.Fatalf("MovePointer(p3) = %v", err)
	}
	expectStaticRoutes(t, w, "shop.example.com", staticRoutesFor("b2"), "the next promotion of the same release finishes the move")
}
