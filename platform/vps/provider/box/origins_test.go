package box_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func heldAfter(calls []string, moved string) bool {
	at := slices.Index(calls, moved)
	return at >= 0 && slices.Contains(calls[at+1:], "hold origins "+slug+"/production")
}

func TestABoundHostnameIsAnOriginTheProjectsBucketsAnswerFromTheBindOn(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	if !heldAfter(stood.calls, "claim shop.example.com") {
		t.Errorf("the box claimed shop.example.com and never held the project's buckets to it: %v. A deploy binds its hostnames after the store stood up, so a browser on the first deploy's own hostname is refused until the next one", stood.calls)
	}
}

func TestAnUnboundHostnameStopsBeingAnOriginTheProjectsBucketsAnswer(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
	ctx := context.Background()
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	if err := stack.UnbindDomain(ctx, "shop.example.com"); err != nil {
		t.Fatalf("UnbindDomain: %v", err)
	}
	if !heldAfter(stood.calls, "disclaim shop.example.com") {
		t.Errorf("the box released shop.example.com and never held the project's buckets to what it still claims: %v, so a page on a name the project gave up keeps its browser access", stood.calls)
	}
}

func TestABucketOriginTheBoxCannotHoldFailsTheBind(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
	stood.refuseOn("HoldOrigins", errors.New("the store answered 503"))
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err == nil {
		t.Error("BindDomain = nil over buckets the box could not hold to the hostname, and the settle reports serving a name whose browsers the store refuses")
	}
}

func TestAPreviewsHostnamesAreOriginsItsProjectsBucketsAnswer(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	stack := previewStack(t, stood)
	previewed(t, stack, "pr-7", "web")

	claimed := "claim " + slug + "--pr-7." + previewBase
	at := slices.Index(stood.calls, claimed)
	if at < 0 || !slices.Contains(stood.calls[at+1:], "hold origins "+slug+"/preview") {
		t.Errorf("the preview claimed its hostname and never held its project's buckets to it: %v", stood.calls)
	}

	if _, err := stack.RemovePointer(context.Background(), "pr-7", edge.DiscardReporter()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	gone := slices.Index(stood.calls, "disclaim "+"ocel--"+slug+"--preview/pr-7")
	if gone < 0 || !slices.Contains(stood.calls[gone+1:], "hold origins "+slug+"/preview") {
		t.Errorf("the preview released its hostnames and never held its project's buckets to what is left: %v", stood.calls)
	}
}

func TestAnUnbindWhoseBucketOriginsCannotBeHeldStillReleasesTheHostnameAndWarns(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
	ctx := context.Background()
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	stood.refuseOn("HoldOrigins", errors.New("the store answered 503"))

	err := stack.UnbindDomain(ctx, "shop.example.com")
	var warned edge.Warning
	if !errors.As(err, &warned) {
		t.Fatalf("UnbindDomain = %v, want a warning: the name is already released on the box, and failing here leaves `domain rm` unable to finish over a CORS rule the next deploy rewrites anyway", err)
	}
	if !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "next deploy") {
		t.Errorf("the warning says %q, want what failed and what repairs it", err)
	}
	if slices.Contains(stack.State().Bound, "shop.example.com") {
		t.Error("the stack still records shop.example.com as bound although the box released it")
	}
}

func TestAPreviewWhoseBucketOriginsCannotBeHeldIsStillRemovedAndWarns(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	stack := previewStack(t, stood)
	previewed(t, stack, "pr-7", "web")
	stood.refuseOn("HoldOrigins", errors.New("the store answered 503"))

	said := &reported{}
	if _, err := stack.RemovePointer(context.Background(), "pr-7", said); err != nil {
		t.Fatalf("RemovePointer = %v, want the preview removed: its hostnames are already released, and a CORS rule the next deploy rewrites is no reason to leave its routes standing", err)
	}
	if !slices.Contains(stood.calls, "unroute ocel--"+slug+"--preview/pr-7") {
		t.Errorf("the preview's routes were never removed: %v", stood.calls)
	}
	if !slices.ContainsFunc(said.lines, func(line string) bool { return strings.Contains(line, "503") }) {
		t.Errorf("the removal said %v, want the origins it could not hold named", said.lines)
	}
}
