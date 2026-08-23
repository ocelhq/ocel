package apigateway

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func deletionTrail(w *world) []string {
	var taken []string
	for _, call := range w.gateway.calls {
		if strings.HasPrefix(call, "DeleteRestApi ") || strings.HasPrefix(call, "hold ") {
			taken = append(taken, call)
		}
	}
	return taken
}

func previewAPIs(t *testing.T, w *world, pointers ...string) []string {
	t.Helper()
	var ids []string
	for _, pointer := range pointers {
		api := w.gateway.named(apiName(conformanceSlug, edge.ClassPreview, pointer))
		if api == nil {
			t.Fatalf("no REST API for %s; the gateway saw %v", pointer, w.gateway.mutations())
		}
		ids = append(ids, api.id)
	}
	return ids
}

func standingAPIs(w *world, ids []string) []string {
	var left []string
	for _, id := range ids {
		if w.gateway.apis[id] != nil {
			left = append(left, id)
		}
	}
	return left
}

func TestDestroySpacesEveryRestAPIDeletionToTheQuota(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	_, stack := previewing(t, w)
	pointers := []string{"pr1", "pr2", "pr3"}
	for _, pointer := range pointers {
		promotePreview(t, stack, pointer)
	}
	ids := previewAPIs(t, w, pointers...)
	w.gateway.calls = nil

	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	taken := deletionTrail(w)
	if len(taken) != 2*len(ids)-1 {
		t.Fatalf("deletions and holds = %v, want %d deletions with one hold between each pair", taken, len(ids))
	}
	for at, call := range taken {
		want := "DeleteRestApi "
		if at%2 == 1 {
			want = "hold " + (30 * time.Second).String()
		}
		if !strings.HasPrefix(call, want) {
			t.Errorf("call %d = %q, want %q: API Gateway allows one deletion every %s per account, and the first one waits on nothing", at, call, want, deleteEvery)
		}
	}
}

func TestRemovingPreviewsOneByOneSpacesThemAgainstEachOther(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	_, stack := previewing(t, w)
	pointers := []string{"pr1", "pr2", "pr3"}
	for _, pointer := range pointers {
		promotePreview(t, stack, pointer)
	}
	ids := previewAPIs(t, w, pointers...)
	w.gateway.calls = nil

	for _, pointer := range pointers {
		if _, err := stack.RemovePointer(ctx, pointer); err != nil {
			t.Fatalf("RemovePointer(%s): %v", pointer, err)
		}
	}

	taken := deletionTrail(w)
	if len(taken) != 2*len(ids)-1 {
		t.Fatalf("deletions and holds = %v, want %d deletions with one hold between each pair; the quota is per account, not per removal", taken, len(ids))
	}
	for at, call := range taken {
		want := "DeleteRestApi "
		if at%2 == 1 {
			want = "hold " + (30 * time.Second).String()
		}
		if !strings.HasPrefix(call, want) {
			t.Errorf("call %d = %q, want %q", at, call, want)
		}
	}
	if standing := standingAPIs(w, ids); len(standing) != 0 {
		t.Errorf("REST APIs still standing = %v, want none", standing)
	}
}

func TestDestroyStopsAtItsBudgetNamingTheRestAPIsStillStanding(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e, stack := previewing(t, w)
	pointers := []string{"pr1", "pr2", "pr3"}
	for _, pointer := range pointers {
		promotePreview(t, stack, pointer)
	}
	ids := previewAPIs(t, w, pointers...)
	e.delete = w.deleter(2)
	w.gateway.calls = nil

	err := stack.Destroy(ctx)
	if err == nil {
		t.Fatal("Destroy error = nil, want the run to stop with work outstanding rather than pretend it finished")
	}

	var outstanding *edge.OutstandingError
	if !errors.As(err, &outstanding) {
		t.Fatalf("Destroy error = %v, want what is left named as outstanding", err)
	}
	left := standingAPIs(w, ids)
	if len(left) != 1 {
		t.Fatalf("REST APIs still standing = %v, want the one the budget did not reach", left)
	}
	if len(outstanding.Items) != 1 || outstanding.Items[0].Kind != kindRestAPI || outstanding.Items[0].Name != left[0] {
		t.Errorf("outstanding items = %+v, want %s %s", outstanding.Items, kindRestAPI, left[0])
	}
	for _, want := range []string{"• " + kindRestAPI + " " + left[0], "re-run the same command"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
	if !slices.ContainsFunc(slices.Collect(maps.Keys(w.dynamo.items)), func(key string) bool {
		return strings.HasPrefix(key, "ledger#")
	}) {
		t.Error("the deployments ledger was erased while REST APIs it names are still standing; a re-run would never find them")
	}

	e.delete = w.deleter(30)
	w.gateway.calls = nil
	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("re-run: %v", err)
	}

	taken := deletionTrail(w)
	if !slices.Equal(taken, []string{"DeleteRestApi " + left[0]}) {
		t.Errorf("the re-run made %v, want it to resume with only what was left and pay the quota for nothing already gone", taken)
	}
	if standing := standingAPIs(w, ids); len(standing) != 0 {
		t.Errorf("REST APIs still standing after the re-run = %v, want none", standing)
	}
}

func TestDeleter(t *testing.T) {
	t.Parallel()

	t.Run("a throttled deletion is another attempt at the same API", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		e := bootstrapped(t, w)
		stack, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		id := ownState(t, stack).API
		w.gateway.deleteErr = &agtypes.TooManyRequestsException{Message: aws.String("Too Many Requests")}
		w.gateway.deleteRefused = 2

		if err := e.deleter().drain(context.Background(), w.clients(), []string{id}); err != nil {
			t.Fatalf("drain: %v", err)
		}
		if w.gateway.apis[id] != nil {
			t.Error("the REST API survived a drain that reported success")
		}
	})

	t.Run("a cancelled command stops deleting and names what it never reached", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		d := &Deleter{
			Wait:     func(context.Context, time.Duration) error { return context.Canceled },
			Attempts: 5,
			Every:    time.Second,
			Jitter:   func() float64 { return 0 },
		}

		err := d.drain(context.Background(), w.clients(), []string{"api1", "api2"})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("drain error = %v, want the cancellation", err)
		}
		if taken := deletionTrail(w); !slices.Equal(taken, []string{"DeleteRestApi api1"}) {
			t.Errorf("the cancelled drain made %v, want it to stop at the first deletion", taken)
		}
		var outstanding *edge.OutstandingError
		if !errors.As(err, &outstanding) {
			t.Fatalf("drain error = %v, want what the cancellation left behind named", err)
		}
		if len(outstanding.Items) != 1 || outstanding.Items[0].Name != "api2" {
			t.Errorf("outstanding items = %+v, want the API the cancellation never reached", outstanding.Items)
		}
	})

	t.Run("an API that refuses to go is still named as standing", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		e := bootstrapped(t, w)
		stack, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		id := ownState(t, stack).API
		w.gateway.deleteErr = errors.New("the account holds a mapping onto it")
		w.gateway.deleteRefused = 1

		err = e.deleter().drain(context.Background(), w.clients(), []string{id})
		if err == nil {
			t.Fatal("drain error = nil, want the refusal reported")
		}
		var outstanding *edge.OutstandingError
		if !errors.As(err, &outstanding) {
			t.Fatalf("drain error = %v, want the API that refused counted as standing", err)
		}
		if len(outstanding.Items) != 1 || outstanding.Items[0].Name != id {
			t.Errorf("outstanding items = %+v, want %s %s", outstanding.Items, kindRestAPI, id)
		}
		if w.gateway.apis[id] == nil {
			t.Error("the REST API is gone, so the drain named something that is not standing")
		}
	})

	t.Run("the jitter never shortens the wait below the quota", func(t *testing.T) {
		t.Parallel()

		d := &Deleter{Every: 30 * time.Second}
		for _, jitter := range []float64{0, 0.5, 0.9999} {
			d.Jitter = func() float64 { return jitter }
			held := d.interval()
			if held < d.every() || held > d.every()+time.Duration(float64(d.every())*deleteJitter) {
				t.Errorf("interval() = %s at jitter %v, want it inside [%s, +%v%%]: the interval is the quota, and a shorter wait is throttled", held, jitter, d.every(), deleteJitter*100)
			}
		}
	})
}
