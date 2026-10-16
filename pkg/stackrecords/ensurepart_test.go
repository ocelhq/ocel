package stackrecords_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type making struct {
	calls     atomic.Int32
	resources []provider.PartResource
	err       error
	hold      chan struct{}
}

func (m *making) work(addedBy, writtenBy, digest string) stackrecords.PartWork {
	return stackrecords.PartWork{
		Name:      "env-source-sync",
		WrittenBy: writtenBy,
		Digest:    digest,
		AddedBy:   addedBy,
		Make: func(ctx context.Context) ([]provider.PartResource, error) {
			m.calls.Add(1)
			if m.hold != nil {
				<-m.hold
			}
			return m.resources, m.err
		},
	}
}

var syncResources = []provider.PartResource{{Kind: "AWS::Lambda::Function", ID: "ocel-env-sync"}}

func TestEnsuringAMissingPartMakesItOnceAndRecordsItReadyAsAddedByItsCaller(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	made := &making{resources: syncResources}

	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, made.work("web", "1.2.0", "d1")); err != nil {
		t.Fatalf("EnsureBootstrapPart() = %v", err)
	}

	got, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "env-source-sync")
	if err != nil || !present || got.State != stackrecords.PartReady || got.AddedBy != "web" || got.Digest != "d1" || len(got.Resources) != 1 {
		t.Errorf("record = %+v, %v, %v, want it ready, added by web, at d1, with what was made", got, present, err)
	}
	if made.calls.Load() != 1 {
		t.Errorf("made %d times, want once", made.calls.Load())
	}
}

func TestEnsuringACurrentPartMakesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	first := &making{resources: syncResources}
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, first.work("web", "1.2.0", "d1")); err != nil {
		t.Fatal(err)
	}

	again := &making{}
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, again.work("api", "1.2.0", "d1")); err != nil {
		t.Fatalf("EnsureBootstrapPart() = %v", err)
	}

	if again.calls.Load() != 0 {
		t.Errorf("a current part was made %d more times", again.calls.Load())
	}
}

func TestEnsuringAPartBehindThisBuildMakesItAgainInPlaceKeepingWhoAddedIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, (&making{}).work("web", "1.2.0", "d1")); err != nil {
		t.Fatal(err)
	}

	moved := &making{resources: syncResources}
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, moved.work("api", "1.3.0", "d2")); err != nil {
		t.Fatalf("EnsureBootstrapPart() = %v", err)
	}

	got, _, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "env-source-sync")
	if moved.calls.Load() != 1 || err != nil || got.State != stackrecords.PartReady || got.Digest != "d2" || got.WrittenBy != "1.3.0" || got.AddedBy != "web" {
		t.Errorf("made %d times, record = %+v, %v; want it made once more, ready at d2 by 1.3.0, still added by web", moved.calls.Load(), got, err)
	}
}

func TestEnsuringAPartANewerBuildWroteLeavesItAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, (&making{}).work("web", "1.3.0", "new")); err != nil {
		t.Fatal(err)
	}

	older := &making{}
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, older.work("api", "1.2.0", "old")); err != nil {
		t.Fatalf("EnsureBootstrapPart() = %v", err)
	}

	got, _, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "env-source-sync")
	if older.calls.Load() != 0 || err != nil || got.Digest != "new" {
		t.Errorf("an older build made it %d times and left %+v (%v), want what 1.3.0 wrote untouched", older.calls.Load(), got, err)
	}
}

func TestConcurrentEnsuresMakeAPartOnceAndEveryOneReturnsOnceItIsReady(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	made := &making{resources: syncResources, hold: make(chan struct{})}

	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i, project := range []string{"web", "api", "docs"} {
		wg.Go(func() {
			errs[i] = stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, made.work(project, "1.2.0", "d1"))
		})
	}
	for made.calls.Load() == 0 {
		waitBriefly()
	}
	close(made.hold)
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatalf("EnsureBootstrapPart() = %v", err)
	}
	if made.calls.Load() != 1 {
		t.Errorf("made %d times by concurrent ensures, want once", made.calls.Load())
	}
}

func TestAFailedMakeReleasesItsClaimSoTheNextEnsureMakesThePart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	failed := &making{err: errors.New("the role is not assumable yet")}

	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, failed.work("web", "1.2.0", "d1")); err == nil {
		t.Fatal("EnsureBootstrapPart() of a failing make = nil error")
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "env-source-sync"); err != nil || present {
		t.Fatalf("record after a failed first make present %v (%v), want the claim released", present, err)
	}

	retried := &making{resources: syncResources}
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, retried.work("web", "1.2.0", "d1")); err != nil || retried.calls.Load() != 1 {
		t.Errorf("EnsureBootstrapPart() after a failed make = %v, made %d times, want it made", err, retried.calls.Load())
	}
}

func TestAFailedMakeOfAPartBehindLeavesTheRecordItMovedFrom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, (&making{resources: syncResources}).work("web", "1.2.0", "d1")); err != nil {
		t.Fatal(err)
	}

	failed := &making{err: errors.New("throttled")}
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, failed.work("api", "1.3.0", "d2")); err == nil {
		t.Fatal("EnsureBootstrapPart() of a failing make = nil error")
	}

	got, _, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "env-source-sync")
	if err != nil || got.State != stackrecords.PartReady || got.Digest != "d1" {
		t.Errorf("record = %+v, %v, want it back as ready at d1", got, err)
	}
}

func TestAClaimLeftCreatingByARunThatStoppedIsTakenOverOnceItsLeaseRunsOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if _, _, err := stackrecords.ClaimBootstrapPart(ctx, store, environment.TierProduction, "env-source-sync", claimOf("web")); err != nil {
		t.Fatal(err)
	}

	made := &making{resources: syncResources}
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, made.work("api", "1.2.0", "d1")); err != nil {
		t.Fatalf("EnsureBootstrapPart() = %v", err)
	}

	got, _, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "env-source-sync")
	if made.calls.Load() != 1 || err != nil || got.State != stackrecords.PartReady {
		t.Errorf("made %d times, record %+v (%v), want the stale claim taken over and the part made", made.calls.Load(), got, err)
	}
}

func TestAFailedTakeoverOfAStaleClaimLeavesNoClaimForOthersToWaitOn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if _, _, err := stackrecords.ClaimBootstrapPart(ctx, store, environment.TierProduction, "env-source-sync", claimOf("web")); err != nil {
		t.Fatal(err)
	}

	failed := &making{err: errors.New("throttled")}
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, failed.work("api", "1.2.0", "d1")); err == nil {
		t.Fatal("EnsureBootstrapPart() of a failing make = nil error")
	}

	if got, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "env-source-sync"); err != nil || present {
		t.Errorf("record after a failed takeover = %+v, present %v (%v), want none: the dead claim put back makes every run wait out a fresh lease", got, present, err)
	}
}

type cancelRefusing struct{ keyvalue.Store }

func (s cancelRefusing) Read(ctx context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	if err := ctx.Err(); err != nil {
		return keyvalue.Entry{}, err
	}
	return s.Store.Read(ctx, key)
}

func (s cancelRefusing) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.Store.Write(ctx, entry)
}

func (s cancelRefusing) Remove(ctx context.Context, key keyvalue.Key, expected keyvalue.Revision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Store.Remove(ctx, key, expected)
}

func cancelledMidMake(t *testing.T, err error) (context.Context, stackrecords.PartWork) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx, stackrecords.PartWork{
		Name:      "env-source-sync",
		WrittenBy: "1.2.0",
		Digest:    "d1",
		AddedBy:   "web",
		Make: func(context.Context) ([]provider.PartResource, error) {
			cancel()
			return syncResources, err
		},
	}
}

func TestAMakeCancelledPartWayReleasesItsClaim(t *testing.T) {
	t.Parallel()
	store := cancelRefusing{fake.NewKeyValues()}
	ctx, work := cancelledMidMake(t, context.Canceled)

	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, work); err == nil {
		t.Fatal("EnsureBootstrapPart() of a cancelled make = nil error")
	}

	if _, present, err := stackrecords.ReadBootstrapPart(context.Background(), store, environment.TierProduction, "env-source-sync"); err != nil || present {
		t.Errorf("record after a cancelled make present %v (%v), want the claim released so the next run need not wait out its lease", present, err)
	}
}

func TestAMakeThatFinishesAsItsCallerIsCancelledStillRecordsThePartReady(t *testing.T) {
	t.Parallel()
	store := cancelRefusing{fake.NewKeyValues()}
	ctx, work := cancelledMidMake(t, nil)

	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, work); err != nil {
		t.Fatalf("EnsureBootstrapPart() = %v", err)
	}

	got, _, err := stackrecords.ReadBootstrapPart(context.Background(), store, environment.TierProduction, "env-source-sync")
	if err != nil || got.State != stackrecords.PartReady {
		t.Errorf("record = %+v (%v), want what was made recorded ready", got, err)
	}
}

func waitBriefly() { time.Sleep(time.Millisecond) }
