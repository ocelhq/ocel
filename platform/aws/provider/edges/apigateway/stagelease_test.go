package apigateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

func heldStage(t *testing.T, w *world, holder string, until time.Time) {
	t.Helper()
	store := awsports.KeyValues{Dynamo: w.dynamo, Tables: awsports.Table(fakeStateTable)}
	partition := keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootRouters, Path: []string{string(Kind), conformanceSlug}}
	value, err := json.Marshal(leaseRecord{Holder: holder, Until: until.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), keyvalue.Entry{Key: partition.Key("stage", router.DefaultPointer), Value: value}); err != nil {
		t.Fatalf("hold the stage: %v", err)
	}
}

func takeStage(t *testing.T, w *world, holder string, until time.Time) {
	t.Helper()
	store := awsports.KeyValues{Dynamo: w.dynamo, Tables: awsports.Table(fakeStateTable)}
	partition := keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootRouters, Path: []string{string(Kind), conformanceSlug}}
	held, err := keyvalue.ReadOrEmpty(context.Background(), store, partition.Key("stage", router.DefaultPointer))
	if err != nil {
		t.Fatalf("read who holds the stage: %v", err)
	}
	if held.Value, err = json.Marshal(leaseRecord{Holder: holder, Until: until.Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), held); err != nil {
		t.Fatalf("take the stage: %v", err)
	}
}

func stageHolder(t *testing.T, w *world) string {
	t.Helper()
	store := awsports.KeyValues{Dynamo: w.dynamo, Tables: awsports.Table(fakeStateTable)}
	partition := keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootRouters, Path: []string{string(Kind), conformanceSlug}}
	held, err := keyvalue.ReadOrEmpty(context.Background(), store, partition.Key("stage", router.DefaultPointer))
	if err != nil {
		t.Fatalf("read who holds the stage: %v", err)
	}
	if len(held.Value) == 0 {
		return ""
	}
	var lease leaseRecord
	if err := json.Unmarshal(held.Value, &lease); err != nil {
		t.Fatal(err)
	}
	return lease.Holder
}

func leasesLeft(w *world) []string {
	var left []string
	for key := range w.dynamo.items {
		if strings.HasPrefix(key, "routers#") {
			left = append(left, key)
		}
	}
	return left
}

func stagedFlip(t *testing.T, stack routerStack, id string) error {
	t.Helper()
	record := router.DeploymentRecord{App: "web", Build: "d1.f1", Entry: "/", EntryFunction: entryFunction, AssetPrefix: "assets/one"}
	return stack.Flip(context.Background(), router.Flip{
		Promotion: router.Promotion{PromotionID: id, Ts: 1, Builds: map[string]string{"web": record.Build}},
		Records:   map[string]router.DeploymentRecord{"web": record},
	}, progress.Discard())
}

func TestAFlipLetsGoOfTheStageOnceItHasMovedIt(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)

	if err := stagedFlip(t, openRouter(stack).Stack.(routerStack), "p1"); err != nil {
		t.Fatalf("Flip: %v", err)
	}
	if left := leasesLeft(w); len(left) != 0 {
		t.Errorf("the flip left %v behind, want the stage let go: a lease nobody releases stalls every later promote until its term runs out", left)
	}
}

func TestAStageAnotherPromoteHoldsIsLeftAloneAndTheFlipIsUnserved(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	heldStage(t, w, "p-other", time.Now().Add(time.Minute))

	err := stagedFlip(t, openRouter(stack).Stack.(routerStack), "p1")
	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("Flip onto a stage another promote holds = %v, want router.Unserved", err)
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Errorf("Flip onto a stage another promote holds = %v, want it refused %s", err, refusal.CodeBusy)
	}
	if got := w.gateway.count("UpdateStage"); got != 0 {
		t.Errorf("UpdateStage calls = %d, want none while another promote holds the stage", got)
	}
}

func TestAStageWhoseHolderNeverLetGoIsTakenOverOnceItsTermIsOver(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	heldStage(t, w, "p-crashed", time.Now().Add(-time.Second))

	if err := stagedFlip(t, openRouter(stack).Stack.(routerStack), "p1"); err != nil {
		t.Fatalf("Flip onto a stage whose lease ran out = %v, want it moved", err)
	}
	if api := w.gateway.named(productionAPIName()); api.variables[entryVariable] != entryFunction {
		t.Errorf("stage variable %s = %q, want %q", entryVariable, api.variables[entryVariable], entryFunction)
	}
}

func TestDestroyLetsGoOfAStageAPromoteNeverReleased(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	heldStage(t, w, "p-crashed", time.Now().Add(time.Minute))

	if err := stack.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if left := leasesLeft(w); len(left) != 0 {
		t.Errorf("the destroy left %v behind, want nothing: a destroyed project leaves no bytes behind", left)
	}
}

func TestAFlipWhoseStageAnotherPromoteTookOverOnceItsTermRanOutMovesNothing(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	w.gateway.mu.Lock()
	w.gateway.beforeGetStage = func() { takeStage(t, w, "p-other", time.Now().Add(time.Minute)) }
	w.gateway.mu.Unlock()

	err := stagedFlip(t, openRouter(stack).Stack.(routerStack), "p1")
	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("Flip whose stage another promote took over = %v, want router.Unserved", err)
	}
	if got := w.gateway.count("UpdateStage"); got != 0 {
		t.Errorf("UpdateStage calls = %d, want none: the stage belongs to the promote that took it over", got)
	}
	if holder := stageHolder(t, w); holder != "p-other" {
		t.Errorf("the stage is held by %q, want p-other still: a flip lets go only of a stage it holds", holder)
	}
}

func TestAFlipWritesTheStageOnlyWhileItsHoldOnTheStageLasts(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)

	started := time.Now()
	if err := stagedFlip(t, openRouter(stack).Stack.(routerStack), "p1"); err != nil {
		t.Fatalf("Flip: %v", err)
	}
	w.gateway.mu.Lock()
	deadline := w.gateway.stageDeadline
	w.gateway.mu.Unlock()
	if deadline.IsZero() || !deadline.Before(started.Add(leaseTerm)) {
		t.Errorf("the stage write ran until %v, want a deadline before %v, when the hold it renewed runs out: a write that outlives the hold can land after another promote took the stage", deadline, started.Add(leaseTerm))
	}
}
