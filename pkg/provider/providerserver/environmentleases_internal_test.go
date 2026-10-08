package providerserver

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	renewedLease = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rivalLease   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var shopProduction = environmentScope{tier: environment.TierProduction, slug: "shop", env: stackrecords.ProductionEnv}

type leaseTimers struct {
	waits chan time.Duration
	fire  chan time.Time

	mu  sync.Mutex
	now time.Time
}

func (l *leaseTimers) after(d time.Duration) <-chan time.Time {
	l.waits <- d
	return l.fire
}

func (l *leaseTimers) readNow() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.now
}

func (l *leaseTimers) advance(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = l.now.Add(d)
}

func (l *leaseTimers) nextWait(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-l.waits:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("the holder never waited for its next renewal")
		return 0
	}
}

func (l *leaseTimers) elapse(t *testing.T, d time.Duration) {
	t.Helper()
	l.advance(d)
	select {
	case l.fire <- l.readNow():
	case <-time.After(10 * time.Second):
		t.Fatal("the holder stopped waiting to renew its lease")
	}
}

func newTimedLeases() (*environmentLeases, *leaseTimers) {
	timers := &leaseTimers{waits: make(chan time.Duration, 16), fire: make(chan time.Time), now: time.Unix(1_700_000_000, 0)}
	leases := &environmentLeases{
		ttl:     5 * time.Minute,
		renewal: 2 * time.Minute,
		retry:   15 * time.Second,
		margin:  time.Minute,
		now:     timers.readNow,
		after:   timers.after,
	}
	return leases, timers
}

type unwritableKeyValues struct {
	keyvalue.Store

	mu      sync.Mutex
	refused bool
}

func (s *unwritableKeyValues) refuseWrites(refused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused = refused
}

func (s *unwritableKeyValues) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	s.mu.Lock()
	refused := s.refused
	s.mu.Unlock()
	if refused {
		return "", errors.New("the store is unreachable")
	}
	return s.Store.Write(ctx, entry)
}

func takeAs(ctx context.Context, store keyvalue.Store, token string) error {
	recorded, err := keyvalue.ReadOrEmpty(ctx, store, stackrecords.EnvironmentLeaseKey(shopProduction.tier, shopProduction.slug, shopProduction.env))
	if err != nil {
		return err
	}
	if recorded.Value, err = json.Marshal(stackrecords.EnvironmentLease{Token: token, ExpiresAt: time.Now().Add(time.Hour).Unix()}); err != nil {
		return err
	}
	_, err = store.Write(ctx, recorded)
	return err
}

func readLeaseRevision(t *testing.T, store keyvalue.Store) keyvalue.Revision {
	t.Helper()
	recorded, err := store.Read(context.Background(), stackrecords.EnvironmentLeaseKey(shopProduction.tier, shopProduction.slug, shopProduction.env))
	if err != nil {
		t.Fatalf("reading the environment lease = %v", err)
	}
	return recorded.Revision
}

func readStopCause(t *testing.T, hold *environmentHold) error {
	t.Helper()
	select {
	case <-hold.context(context.Background()).Done():
		return context.Cause(hold.context(context.Background()))
	case <-time.After(10 * time.Second):
		t.Fatal("the holder's work was never stopped")
		return nil
	}
}

func TestAHeldLeaseIsRenewedWhileItsHolderRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	leases, timers := newTimedLeases()
	hold, err := leases.take(ctx, store, shopProduction, renewedLease)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.release(ctx) }()
	taken := readLeaseRevision(t, store)

	if wait := timers.nextWait(t); wait != leases.renewal {
		t.Fatalf("the holder waited %s before renewing, want %s", wait, leases.renewal)
	}
	timers.elapse(t, leases.renewal)
	timers.nextWait(t)

	if readLeaseRevision(t, store) == taken {
		t.Error("the lease was never rewritten, want a running holder to renew it")
	}
}

func TestAHolderRetriesAFailedRenewalSoonerThanItsNextRegularRenewal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &unwritableKeyValues{Store: fake.NewKeyValues()}
	leases, timers := newTimedLeases()
	hold, err := leases.take(ctx, store, shopProduction, renewedLease)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.release(ctx) }()
	timers.nextWait(t)
	store.refuseWrites(true)

	timers.elapse(t, leases.renewal)

	if wait := timers.nextWait(t); wait != leases.retry {
		t.Errorf("after a failed renewal the holder waited %s, want %s: two missed regular renewals would let the lease run out", wait, leases.retry)
	}
}

func TestAHolderThatCannotRenewForItsWholeWindowHasItsWorkStopped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &unwritableKeyValues{Store: fake.NewKeyValues()}
	leases, timers := newTimedLeases()
	hold, err := leases.take(ctx, store, shopProduction, renewedLease)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.release(ctx) }()
	store.refuseWrites(true)
	go func() {
		for {
			select {
			case <-timers.waits:
			case <-hold.done:
				return
			}
			timers.advance(leases.retry)
			select {
			case timers.fire <- timers.readNow():
			case <-hold.done:
				return
			}
		}
	}()

	cause := readStopCause(t, hold)

	if !isBusy(cause) {
		t.Errorf("the holder's work stopped with %v, want a busy refusal: once it cannot renew for its whole window another deploy may take the lease over", cause)
	}
}

func TestAHolderWhoseLeaseWasTakenOverHasItsWorkStoppedAndNeverTakesItBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	leases, timers := newTimedLeases()
	hold, err := leases.take(ctx, store, shopProduction, renewedLease)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.release(ctx) }()
	timers.nextWait(t)
	if err := takeAs(ctx, store, rivalLease); err != nil {
		t.Fatal(err)
	}

	timers.elapse(t, leases.renewal)

	if cause := readStopCause(t, hold); !isBusy(cause) {
		t.Errorf("the holder's work stopped with %v, want a busy refusal: another deploy took its lease over", cause)
	}
	if err := stackrecords.ForgetEnvironmentLease(ctx, store, shopProduction.tier, shopProduction.slug, shopProduction.env, rivalLease); err != nil {
		t.Fatal(err)
	}
	<-hold.done
	if _, err := store.Read(ctx, stackrecords.EnvironmentLeaseKey(shopProduction.tier, shopProduction.slug, shopProduction.env)); !errors.Is(err, keyvalue.ErrNotFound) {
		t.Errorf("reading the lease after the deploy that took it over freed it = %v, want none: the holder that lost it must not take it back", err)
	}
}

func TestAHolderStopsRenewingWhenTheRequestThatHoldsItEnds(t *testing.T) {
	t.Parallel()
	store := fake.NewKeyValues()
	leases, timers := newTimedLeases()
	request, end := context.WithCancel(context.Background())
	hold, err := leases.take(request, store, shopProduction, renewedLease)
	if err != nil {
		t.Fatal(err)
	}
	timers.nextWait(t)
	taken := readLeaseRevision(t, store)

	end()

	select {
	case <-hold.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the holder kept renewing after the request that held the lease ended, want the lease left to run out")
	}
	if readLeaseRevision(t, store) != taken {
		t.Error("the lease was rewritten after the request that held it ended")
	}
}
