package stackrecords_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	leaseTTL   = 5 * time.Minute
	leaseWatch = 30 * time.Second
	firstLease = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	otherLease = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var leaseStart = time.Unix(1_700_000_000, 0)

type leaseClock struct {
	now      time.Time
	waited   time.Duration
	watching func()
}

func clockAt(now time.Time) *leaseClock { return &leaseClock{now: now} }

func (c *leaseClock) terms() stackrecords.LeaseTerms {
	return stackrecords.LeaseTerms{
		TTL:   leaseTTL,
		Watch: leaseWatch,
		Now:   func() time.Time { return c.now },
		Wait: func(context.Context, time.Duration) error {
			return errors.New("waited on a lease that was free or held")
		},
	}
}

func (c *leaseClock) watchingTerms() stackrecords.LeaseTerms {
	terms := c.terms()
	terms.Wait = func(_ context.Context, d time.Duration) error {
		c.now, c.waited = c.now.Add(d), c.waited+d
		if c.watching != nil {
			c.watching()
		}
		return nil
	}
	return terms
}

func takeLease(store keyvalue.Store, token string, terms stackrecords.LeaseTerms) error {
	_, err := stackrecords.TakeEnvironmentLease(context.Background(), store, environment.TierProduction, "shop", "production", token, stackrecords.LeaseDeploy, terms)
	return err
}

func renewLease(store keyvalue.Store, token string, terms stackrecords.LeaseTerms) error {
	return stackrecords.RenewEnvironmentLease(context.Background(), store, environment.TierProduction, "shop", "production", token, stackrecords.LeaseDeploy, terms)
}

func isBusy(err error) bool {
	var refused refusal.Refusal
	return errors.As(err, &refused) && refused.Code == refusal.CodeBusy
}

func TestTakeEnvironmentLeaseIsRefusedWhileAnotherDeployHoldsTheEnvironment(t *testing.T) {
	t.Parallel()
	store := fake.NewKeyValues()
	if err := takeLease(store, firstLease, clockAt(leaseStart).terms()); err != nil {
		t.Fatalf("TakeEnvironmentLease: %v", err)
	}

	err := takeLease(store, otherLease, clockAt(leaseStart.Add(time.Minute)).terms())

	if !isBusy(err) {
		t.Errorf("TakeEnvironmentLease = %v, want a busy refusal: another deploy holds production", err)
	}
}

func TestTakeEnvironmentLeaseRefusalNamesWhenTheLeaseRunsOutInUTC(t *testing.T) {
	t.Parallel()
	store := fake.NewKeyValues()
	if err := takeLease(store, firstLease, clockAt(leaseStart).terms()); err != nil {
		t.Fatal(err)
	}

	err := takeLease(store, otherLease, clockAt(leaseStart.Add(time.Minute)).terms())

	if err == nil || !strings.Contains(err.Error(), "2023-11-14 22:18:20 UTC") {
		t.Errorf("TakeEnvironmentLease = %v, want the refusal to name 2023-11-14 22:18:20 UTC, when the lease runs out, whatever the deployer's time zone", err)
	}
}

func TestTakeEnvironmentLeaseReportsWhetherTheTokenAlreadyHeldTheLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	first, err := stackrecords.TakeEnvironmentLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, stackrecords.LeaseDeploy, clockAt(leaseStart).terms())
	if err != nil {
		t.Fatal(err)
	}
	again, err := stackrecords.TakeEnvironmentLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, stackrecords.LeaseDeploy, clockAt(leaseStart.Add(time.Minute)).terms())
	if err != nil {
		t.Fatal(err)
	}

	if first || !again {
		t.Errorf("TakeEnvironmentLease reported held = %t, then %t, want false for the take that found production free and true for the one that found it held under the same token", first, again)
	}
}

func TestTakeEnvironmentLeaseWatchesALeaseThatLooksRunOutForAFullTTLBeforeTakingIt(t *testing.T) {
	t.Parallel()
	store := fake.NewKeyValues()
	if err := takeLease(store, firstLease, clockAt(leaseStart).terms()); err != nil {
		t.Fatal(err)
	}
	clock := clockAt(leaseStart.Add(leaseTTL))

	err := takeLease(store, otherLease, clock.watchingTerms())

	if err != nil {
		t.Fatalf("TakeEnvironmentLease = %v, want the lease of a deploy that stopped renewing taken over", err)
	}
	if clock.waited < leaseTTL {
		t.Errorf("TakeEnvironmentLease took the lease after watching it for %s, want at least %s: only a lease nobody renewed for a whole TTL on the taker's own clock has run out", clock.waited, leaseTTL)
	}
}

func TestTakeEnvironmentLeaseIsRefusedWhenTheHolderRenewsWhileATakerWhoseClockRunsAheadWatches(t *testing.T) {
	t.Parallel()
	store := fake.NewKeyValues()
	if err := takeLease(store, firstLease, clockAt(leaseStart).terms()); err != nil {
		t.Fatal(err)
	}
	ahead := clockAt(leaseStart.Add(time.Hour))
	ahead.watching = func() {
		if err := renewLease(store, firstLease, clockAt(leaseStart.Add(2*time.Minute)).terms()); err != nil {
			t.Errorf("renewing the holder's lease = %v", err)
		}
	}

	err := takeLease(store, otherLease, ahead.watchingTerms())

	if !isBusy(err) {
		t.Errorf("TakeEnvironmentLease = %v, want a busy refusal: the holder renewed while the taker watched, so its lease is live whatever the taker's clock says", err)
	}
}

func TestTakeEnvironmentLeaseKeepsEachEnvironmentApart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if _, err := stackrecords.TakeEnvironmentLease(ctx, store, environment.TierPreview, "shop", "pr-12", firstLease, stackrecords.LeaseDeploy, clockAt(leaseStart).terms()); err != nil {
		t.Fatal(err)
	}

	for _, other := range []struct {
		tier      environment.Tier
		slug, env string
	}{
		{environment.TierPreview, "shop", "pr-13"},
		{environment.TierPreview, "blog", "pr-12"},
		{environment.TierProduction, "shop", "pr-12"},
	} {
		if _, err := stackrecords.TakeEnvironmentLease(ctx, store, other.tier, other.slug, other.env, otherLease, stackrecords.LeaseDeploy, clockAt(leaseStart).terms()); err != nil {
			t.Errorf("TakeEnvironmentLease(%s %s %s) = %v, want it free: only pr-12 of shop's previews is held", other.tier, other.slug, other.env, err)
		}
	}
}

func TestForgetEnvironmentLeaseFreesTheEnvironmentForTheNextDeploy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := takeLease(store, firstLease, clockAt(leaseStart).terms()); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.ForgetEnvironmentLease(ctx, store, environment.TierProduction, "shop", "production", firstLease); err != nil {
		t.Fatalf("ForgetEnvironmentLease: %v", err)
	}

	err := takeLease(store, otherLease, clockAt(leaseStart).terms())

	if err != nil {
		t.Errorf("TakeEnvironmentLease = %v, want production free once its holder forgot the lease", err)
	}
}

func TestForgetEnvironmentLeaseKeepsALeaseAnotherDeployHolds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := takeLease(store, otherLease, clockAt(leaseStart).terms()); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.ForgetEnvironmentLease(ctx, store, environment.TierProduction, "shop", "production", firstLease); err != nil {
		t.Fatalf("ForgetEnvironmentLease: %v", err)
	}

	err := takeLease(store, firstLease, clockAt(leaseStart).terms())

	if !isBusy(err) {
		t.Errorf("TakeEnvironmentLease = %v, want a busy refusal: forgetting a lease it never held must not free the other deploy's", err)
	}
}

func TestRenewEnvironmentLeaseIsRefusedOnceAnotherDeployTookTheLeaseOver(t *testing.T) {
	t.Parallel()
	store := fake.NewKeyValues()
	if err := takeLease(store, firstLease, clockAt(leaseStart).terms()); err != nil {
		t.Fatal(err)
	}
	if err := takeLease(store, otherLease, clockAt(leaseStart.Add(leaseTTL)).watchingTerms()); err != nil {
		t.Fatal(err)
	}

	err := renewLease(store, firstLease, clockAt(leaseStart.Add(leaseTTL)).terms())

	if !isBusy(err) {
		t.Errorf("RenewEnvironmentLease = %v, want a busy refusal: another deploy took the lease over", err)
	}
}

func TestRenewEnvironmentLeaseDoesNotTakeBackALeaseTheDeployThatTookItOverFreed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := takeLease(store, otherLease, clockAt(leaseStart).terms()); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.ForgetEnvironmentLease(ctx, store, environment.TierProduction, "shop", "production", otherLease); err != nil {
		t.Fatal(err)
	}

	err := renewLease(store, firstLease, clockAt(leaseStart).terms())

	if !isBusy(err) {
		t.Errorf("RenewEnvironmentLease = %v, want a busy refusal: the deploy no longer holds the lease, so whatever ran since may have changed the environment", err)
	}
}

func TestRenewEnvironmentLeaseKeepsTheHolderPastItsFirstExpiry(t *testing.T) {
	t.Parallel()
	store := fake.NewKeyValues()
	if err := takeLease(store, firstLease, clockAt(leaseStart).terms()); err != nil {
		t.Fatal(err)
	}
	if err := renewLease(store, firstLease, clockAt(leaseStart.Add(4*time.Minute)).terms()); err != nil {
		t.Fatalf("RenewEnvironmentLease: %v", err)
	}

	err := takeLease(store, otherLease, clockAt(leaseStart.Add(8*time.Minute)).terms())

	if !isBusy(err) {
		t.Errorf("TakeEnvironmentLease = %v, want a busy refusal: the renewal moved the expiry to nine minutes in", err)
	}
}

func TestTakeEnvironmentLeaseRefusalNamesWhatHoldsTheEnvironmentAndWhatToRunAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if _, err := stackrecords.TakeEnvironmentLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, stackrecords.LeaseRollback, clockAt(leaseStart).terms()); err != nil {
		t.Fatal(err)
	}

	_, err := stackrecords.TakeEnvironmentLease(ctx, store, environment.TierProduction, "shop", "production", otherLease, stackrecords.LeaseRemoval, clockAt(leaseStart).terms())

	if err == nil || !strings.Contains(err.Error(), "a rollback of production is running: remove it again once it ends") {
		t.Errorf("TakeEnvironmentLease = %v, want the refusal to say a rollback of production holds it and to remove it again once that ends", err)
	}
}
