package providerserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	if recorded.Value, err = json.Marshal(stackrecords.EnvironmentLease{Token: token, Holder: stackrecords.LeaseDeploy, ExpiresAt: time.Now().Add(time.Hour).Unix()}); err != nil {
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
	hold, err := leases.take(ctx, store, shopProduction, renewedLease, stackrecords.LeaseDeploy)
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
	hold, err := leases.take(ctx, store, shopProduction, renewedLease, stackrecords.LeaseDeploy)
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
	hold, err := leases.take(ctx, store, shopProduction, renewedLease, stackrecords.LeaseDeploy)
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
	hold, err := leases.take(ctx, store, shopProduction, renewedLease, stackrecords.LeaseDeploy)
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
	hold, err := leases.take(request, store, shopProduction, renewedLease, stackrecords.LeaseDeploy)
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

type leaseAlarm struct {
	at   time.Time
	fire chan time.Time
}

type leaseClock struct {
	mu      sync.Mutex
	now     time.Time
	alarms  []leaseAlarm
	alarmed time.Time
}

func (c *leaseClock) readNow() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *leaseClock) after(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	fire := make(chan time.Time, 1)
	c.alarms = append(c.alarms, leaseAlarm{at: c.now.Add(d), fire: fire})
	c.alarmed = time.Now()
	return fire
}

func (c *leaseClock) advanceToNextAlarm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.alarms) == 0 || time.Since(c.alarmed) < 20*time.Millisecond {
		return
	}
	next := c.alarms[0].at
	for _, alarm := range c.alarms {
		if alarm.at.Before(next) {
			next = alarm.at
		}
	}
	c.now = next
	pending := c.alarms[:0]
	for _, alarm := range c.alarms {
		if alarm.at.After(c.now) {
			pending = append(pending, alarm)
			continue
		}
		alarm.fire <- c.now
	}
	c.alarms = pending
}

func (c *leaseClock) run(until <-chan struct{}) {
	for {
		select {
		case <-until:
			return
		case <-time.After(time.Millisecond):
		}
		c.advanceToNextAlarm()
	}
}

func newClockedLeases() (*environmentLeases, *leaseClock) {
	clock := &leaseClock{now: time.Unix(1_700_000_000, 0)}
	leases := &environmentLeases{
		ttl:     5 * time.Minute,
		renewal: 2 * time.Minute,
		retry:   15 * time.Second,
		margin:  time.Minute,
		now:     clock.readNow,
		after:   clock.after,
	}
	return leases, clock
}

func previewScopes(n int) []leaseSubject {
	scopes := make([]leaseSubject, 0, n)
	for i := range n {
		scopes = append(scopes, environmentScope{tier: environment.TierPreview, slug: "shop", env: fmt.Sprintf("pr-%d", i)})
	}
	return scopes
}

func previewLeaseKey(scope leaseSubject) keyvalue.Key {
	env := scope.(environmentScope)
	return stackrecords.EnvironmentLeaseKey(env.tier, env.slug, env.env)
}

func recordRivalLease(t *testing.T, store keyvalue.Store, scope leaseSubject, expiresAt time.Time) {
	t.Helper()
	ctx := context.Background()
	recorded, err := keyvalue.ReadOrEmpty(ctx, store, previewLeaseKey(scope))
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Value, err = json.Marshal(stackrecords.EnvironmentLease{Token: rivalLease, Holder: stackrecords.LeaseDeploy, ExpiresAt: expiresAt.Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, recorded); err != nil {
		t.Fatal(err)
	}
}

func readLeaseToken(t *testing.T, store keyvalue.Store, scope leaseSubject) string {
	t.Helper()
	recorded, err := keyvalue.ReadOrEmpty(context.Background(), store, previewLeaseKey(scope))
	if err != nil {
		t.Fatal(err)
	}
	var held stackrecords.EnvironmentLease
	if len(recorded.Value) > 0 {
		if err := json.Unmarshal(recorded.Value, &held); err != nil {
			t.Fatal(err)
		}
	}
	return held.Token
}

type renewingKeyValues struct {
	keyvalue.Store

	mu      sync.Mutex
	renewed string
	reads   int
}

func (s *renewingKeyValues) renewOnRead(key keyvalue.Key, read int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewed, s.reads = key.String(), read
}

func (s *renewingKeyValues) Read(ctx context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	s.mu.Lock()
	renew := false
	if key.String() == s.renewed && s.reads > 0 {
		s.reads--
		renew = s.reads == 0
	}
	s.mu.Unlock()
	if renew {
		recorded, err := s.Store.Read(ctx, key)
		if err != nil {
			return keyvalue.Entry{}, err
		}
		if recorded.Value, err = json.Marshal(stackrecords.EnvironmentLease{Token: rivalLease, Holder: stackrecords.LeaseDeploy, ExpiresAt: time.Now().Add(time.Hour).Unix()}); err != nil {
			return keyvalue.Entry{}, err
		}
		if _, err := s.Write(ctx, recorded); err != nil {
			return keyvalue.Entry{}, err
		}
	}
	return s.Store.Read(ctx, key)
}

func TestTakingTheLeasesOfManyInterruptedRunsWatchesThemAllWithinOneTTL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	leases, clock := newClockedLeases()
	scopes := previewScopes(20)
	started := clock.readNow()
	for _, scope := range scopes {
		recordRivalLease(t, store, scope, started.Add(-time.Minute))
	}
	taken := make(chan struct{})
	go clock.run(taken)

	holds, err := leases.takeEach(ctx, store, renewedLease, scopes, stackrecords.LeaseRemoval)
	close(taken)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holds.release(ctx) }()

	if waited := clock.readNow().Sub(started); waited > 2*leases.ttl {
		t.Errorf("taking %d run-out leases waited %s, want about one TTL (%s): every lease is watched at the same time", len(scopes), waited, leases.ttl)
	}
	for _, scope := range scopes {
		if token := readLeaseToken(t, store, scope); token != renewedLease {
			t.Errorf("%s is held by %q after the removal took every lease, want the removal's token", scope.describe(), token)
		}
	}
}

func TestTakingManyLeasesIsRefusedWhenOneIsRenewedWhileWatchedAndFreesEveryLeaseItTook(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &renewingKeyValues{Store: fake.NewKeyValues()}
	leases, clock := newClockedLeases()
	scopes := previewScopes(6)
	for _, scope := range scopes[3:] {
		recordRivalLease(t, store, scope, clock.readNow().Add(-time.Minute))
	}
	store.renewOnRead(previewLeaseKey(scopes[4]), 3)
	taken := make(chan struct{})
	go clock.run(taken)

	_, err := leases.takeEach(ctx, store, renewedLease, scopes, stackrecords.LeaseRemoval)
	close(taken)

	if !isBusy(err) {
		t.Fatalf("takeEach() = %v, want a busy refusal: the deploy holding pr-4 still renews it", err)
	}
	for _, scope := range scopes {
		if token := readLeaseToken(t, store, scope); token == renewedLease {
			t.Errorf("%s is still held by the refused removal, want every lease it took freed", scope.describe())
		}
	}
}

func TestLosingAnyOfManyHeldLeasesStopsTheWorkTheyHold(t *testing.T) {
	t.Parallel()
	for _, lost := range []int{0, 2, 4} {
		t.Run(fmt.Sprintf("pr-%d", lost), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := fake.NewKeyValues()
			leases, clock := newClockedLeases()
			scopes := previewScopes(5)
			holds, err := leases.takeEach(ctx, store, renewedLease, scopes, stackrecords.LeaseRemoval)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = holds.release(ctx) }()
			recordRivalLease(t, store, scopes[lost], clock.readNow().Add(time.Hour))
			stopped := make(chan struct{})
			defer close(stopped)
			go clock.run(stopped)

			held := holds.context()
			select {
			case <-held.Done():
			case <-time.After(10 * time.Second):
				t.Fatalf("the work held by %d leases kept running after another deploy took %s over", len(scopes), scopes[lost].describe())
			}
			if cause := context.Cause(held); !isBusy(cause) {
				t.Errorf("the work stopped with %v, want the busy refusal of the lost lease", cause)
			}
		})
	}
}
