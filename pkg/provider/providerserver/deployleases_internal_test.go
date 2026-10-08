package providerserver

import (
	"context"
	"encoding/json"
	"errors"
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

func quickLeases() *deployLeases {
	return &deployLeases{ttl: 3 * time.Second, renewal: 20 * time.Millisecond, renewing: map[string]*leaseRenewal{}}
}

func takeAs(ctx context.Context, store keyvalue.Store, token string, now time.Time) error {
	return stackrecords.TakeDeployLease(ctx, store, shopProduction.tier, shopProduction.slug, shopProduction.env, token, now, time.Hour)
}

func readLeaseExpiry(t *testing.T, store keyvalue.Store) int64 {
	t.Helper()
	recorded, err := store.Read(context.Background(), stackrecords.DeployLeaseKey(shopProduction.tier, shopProduction.slug, shopProduction.env))
	if err != nil {
		t.Fatalf("reading the deploy lease = %v", err)
	}
	var held stackrecords.DeployLease
	if err := json.Unmarshal(recorded.Value, &held); err != nil {
		t.Fatal(err)
	}
	return held.ExpiresAt
}

func TestAHeldDeployLeaseIsRenewedWhileItsDeployRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	leases := quickLeases()
	if err := leases.hold(ctx, store, shopProduction, renewedLease); err != nil {
		t.Fatal(err)
	}
	defer leases.release(ctx, store, shopProduction, renewedLease)
	taken := readLeaseExpiry(t, store)

	deadline := time.Now().Add(10 * time.Second)
	for readLeaseExpiry(t, store) <= taken {
		if time.Now().After(deadline) {
			t.Fatal("the lease expiry never moved, want a running deploy to keep renewing it")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestADeployThatLostItsLeaseDoesNotRenewItOverTheDeployThatTookItOver(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	leases := quickLeases()
	if err := leases.hold(ctx, store, shopProduction, renewedLease); err != nil {
		t.Fatal(err)
	}
	defer leases.release(ctx, store, shopProduction, renewedLease)
	if err := takeAs(ctx, store, rivalLease, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	leases.mu.Lock()
	renewal := leases.renewing[leaseName(shopProduction, renewedLease)]
	leases.mu.Unlock()
	select {
	case <-renewal.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the deploy that lost its lease kept renewing it")
	}
	if err := stackrecords.ForgetDeployLease(ctx, store, shopProduction.tier, shopProduction.slug, shopProduction.env, rivalLease); err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	_, err := store.Read(ctx, stackrecords.DeployLeaseKey(shopProduction.tier, shopProduction.slug, shopProduction.env))
	if !errors.Is(err, keyvalue.ErrNotFound) {
		t.Errorf("reading the lease after the deploy that took it over ended = %v, want none: the deploy that lost it must not take it back", err)
	}
}
