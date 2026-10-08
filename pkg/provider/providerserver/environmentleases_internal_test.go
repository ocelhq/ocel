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

func quickLeases() *environmentLeases {
	return &environmentLeases{ttl: 3 * time.Second, renewal: 20 * time.Millisecond, renewing: map[heldLease]*leaseRenewal{}}
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

func readLeaseExpiry(t *testing.T, store keyvalue.Store) int64 {
	t.Helper()
	recorded, err := store.Read(context.Background(), stackrecords.EnvironmentLeaseKey(shopProduction.tier, shopProduction.slug, shopProduction.env))
	if err != nil {
		t.Fatalf("reading the deploy lease = %v", err)
	}
	var held stackrecords.EnvironmentLease
	if err := json.Unmarshal(recorded.Value, &held); err != nil {
		t.Fatal(err)
	}
	return held.ExpiresAt
}

func TestAHeldEnvironmentLeaseIsRenewedWhileItsDeployRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	leases := quickLeases()
	if _, err := leases.hold(ctx, store, shopProduction, renewedLease); err != nil {
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

func TestALeaseHeldAgainAfterItWasLostIsRenewedAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	leases := quickLeases()
	if _, err := leases.hold(ctx, store, shopProduction, renewedLease); err != nil {
		t.Fatal(err)
	}
	defer leases.release(ctx, store, shopProduction, renewedLease)
	if err := takeAs(ctx, store, rivalLease); err != nil {
		t.Fatal(err)
	}
	leases.mu.Lock()
	lost := leases.renewing[heldLease{scope: shopProduction, token: renewedLease}]
	leases.mu.Unlock()
	<-lost.done
	if err := stackrecords.ForgetEnvironmentLease(ctx, store, shopProduction.tier, shopProduction.slug, shopProduction.env, rivalLease); err != nil {
		t.Fatal(err)
	}

	if _, err := leases.hold(ctx, store, shopProduction, renewedLease); err != nil {
		t.Fatal(err)
	}
	taken := readLeaseExpiry(t, store)

	deadline := time.Now().Add(10 * time.Second)
	for readLeaseExpiry(t, store) <= taken {
		if time.Now().After(deadline) {
			t.Fatal("the lease expiry never moved, want the deploy that held it again to renew it again")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestADeployThatLostItsLeaseDoesNotRenewItOverTheDeployThatTookItOver(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	leases := quickLeases()
	if _, err := leases.hold(ctx, store, shopProduction, renewedLease); err != nil {
		t.Fatal(err)
	}
	defer leases.release(ctx, store, shopProduction, renewedLease)
	if err := takeAs(ctx, store, rivalLease); err != nil {
		t.Fatal(err)
	}
	leases.mu.Lock()
	renewal := leases.renewing[heldLease{scope: shopProduction, token: renewedLease}]
	leases.mu.Unlock()
	select {
	case <-renewal.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the deploy that lost its lease kept renewing it")
	}
	if err := stackrecords.ForgetEnvironmentLease(ctx, store, shopProduction.tier, shopProduction.slug, shopProduction.env, rivalLease); err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	_, err := store.Read(ctx, stackrecords.EnvironmentLeaseKey(shopProduction.tier, shopProduction.slug, shopProduction.env))
	if !errors.Is(err, keyvalue.ErrNotFound) {
		t.Errorf("reading the lease after the deploy that took it over ended = %v, want none: the deploy that lost it must not take it back", err)
	}
}
