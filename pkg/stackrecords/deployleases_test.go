package stackrecords_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	leaseTTL   = 5 * time.Minute
	firstLease = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	otherLease = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var leaseStart = time.Unix(1_700_000_000, 0)

func isBusy(err error) bool {
	var refused refusal.Refusal
	return errors.As(err, &refused) && refused.Code == refusal.CodeBusy
}

func TestTakeDeployLeaseIsRefusedWhileAnotherDeployHoldsTheEnvironment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, leaseStart, leaseTTL); err != nil {
		t.Fatalf("TakeDeployLease: %v", err)
	}

	err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", otherLease, leaseStart.Add(time.Minute), leaseTTL)

	if !isBusy(err) {
		t.Errorf("TakeDeployLease = %v, want a busy refusal: another deploy holds production", err)
	}
}

func TestTakeDeployLeaseRefusalNamesWhenTheLeaseRunsOutInUTC(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, leaseStart, leaseTTL); err != nil {
		t.Fatal(err)
	}

	err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", otherLease, leaseStart.Add(time.Minute), leaseTTL)

	if err == nil || !strings.Contains(err.Error(), "2023-11-14 22:18:20 UTC") {
		t.Errorf("TakeDeployLease = %v, want the refusal to name 2023-11-14 22:18:20 UTC, when the lease runs out, whatever the deployer's time zone", err)
	}
}

func TestTakeDeployLeaseLetsTheHolderRenewItsLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, leaseStart, leaseTTL); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, leaseStart.Add(4*time.Minute), leaseTTL); err != nil {
		t.Fatalf("renewing TakeDeployLease: %v", err)
	}

	err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", otherLease, leaseStart.Add(8*time.Minute), leaseTTL)

	if !isBusy(err) {
		t.Errorf("TakeDeployLease = %v, want a busy refusal: the renewal moved the expiry to nine minutes in", err)
	}
}

func TestTakeDeployLeaseTakesOverALeaseThatExpired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, leaseStart, leaseTTL); err != nil {
		t.Fatal(err)
	}

	err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", otherLease, leaseStart.Add(leaseTTL), leaseTTL)

	if err != nil {
		t.Errorf("TakeDeployLease = %v, want the lease of a deploy that stopped renewing taken over", err)
	}
}

func TestTakeDeployLeaseKeepsEachEnvironmentApart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.TakeDeployLease(ctx, store, environment.TierPreview, "shop", "pr-12", firstLease, leaseStart, leaseTTL); err != nil {
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
		if err := stackrecords.TakeDeployLease(ctx, store, other.tier, other.slug, other.env, otherLease, leaseStart, leaseTTL); err != nil {
			t.Errorf("TakeDeployLease(%s %s %s) = %v, want it free: only pr-12 of shop's previews is held", other.tier, other.slug, other.env, err)
		}
	}
}

func TestForgetDeployLeaseFreesTheEnvironmentForTheNextDeploy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, leaseStart, leaseTTL); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.ForgetDeployLease(ctx, store, environment.TierProduction, "shop", "production", firstLease); err != nil {
		t.Fatalf("ForgetDeployLease: %v", err)
	}

	err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", otherLease, leaseStart, leaseTTL)

	if err != nil {
		t.Errorf("TakeDeployLease = %v, want production free once its holder forgot the lease", err)
	}
}

func TestForgetDeployLeaseKeepsALeaseAnotherDeployHolds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", otherLease, leaseStart, leaseTTL); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.ForgetDeployLease(ctx, store, environment.TierProduction, "shop", "production", firstLease); err != nil {
		t.Fatalf("ForgetDeployLease: %v", err)
	}

	err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, leaseStart, leaseTTL)

	if !isBusy(err) {
		t.Errorf("TakeDeployLease = %v, want a busy refusal: forgetting a lease it never held must not free the other deploy's", err)
	}
}

func TestRenewDeployLeaseIsRefusedOnceAnotherDeployTookTheLeaseOver(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, leaseStart, leaseTTL); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", otherLease, leaseStart.Add(leaseTTL), leaseTTL); err != nil {
		t.Fatal(err)
	}

	err := stackrecords.RenewDeployLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, leaseStart.Add(leaseTTL), leaseTTL)

	if !isBusy(err) {
		t.Errorf("RenewDeployLease = %v, want a busy refusal: another deploy took the lease over", err)
	}
}

func TestRenewDeployLeaseDoesNotTakeBackALeaseTheDeployThatTookItOverFreed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", otherLease, leaseStart, leaseTTL); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.ForgetDeployLease(ctx, store, environment.TierProduction, "shop", "production", otherLease); err != nil {
		t.Fatal(err)
	}

	err := stackrecords.RenewDeployLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, leaseStart, leaseTTL)

	if !isBusy(err) {
		t.Errorf("RenewDeployLease = %v, want a busy refusal: the deploy no longer holds the lease, so whatever ran since may have changed the environment", err)
	}
}

func TestRenewDeployLeaseKeepsTheHolderPastItsFirstExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, leaseStart, leaseTTL); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.RenewDeployLease(ctx, store, environment.TierProduction, "shop", "production", firstLease, leaseStart.Add(4*time.Minute), leaseTTL); err != nil {
		t.Fatalf("RenewDeployLease: %v", err)
	}

	err := stackrecords.TakeDeployLease(ctx, store, environment.TierProduction, "shop", "production", otherLease, leaseStart.Add(8*time.Minute), leaseTTL)

	if !isBusy(err) {
		t.Errorf("TakeDeployLease = %v, want a busy refusal: the renewal moved the expiry to nine minutes in", err)
	}
}
