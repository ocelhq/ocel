package envsource_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

type listing struct {
	in    records.Partition
	under []string
}

type watchedKeyValues struct {
	records.Store

	mu            sync.Mutex
	listed        []listing
	staleIn       records.Root
	failIn        records.Root
	failRemovesIn records.Root
}

func (w *watchedKeyValues) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	w.mu.Lock()
	w.listed = append(w.listed, listing{in: in, under: under})
	w.mu.Unlock()
	return w.Store.List(ctx, in, under...)
}

func (w *watchedKeyValues) Read(ctx context.Context, key records.Key) (records.Entry, error) {
	if w.failIn != "" && key.Partition.Root == w.failIn {
		return records.Entry{}, errors.New("the table is unreachable")
	}
	return w.Store.Read(ctx, key)
}

func (w *watchedKeyValues) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
	if w.staleIn != "" && entry.Key.Partition.Root == w.staleIn {
		return "", records.ErrStale
	}
	return w.Store.Write(ctx, entry)
}

func (w *watchedKeyValues) Remove(ctx context.Context, key records.Key, expected records.Revision) error {
	w.mu.Lock()
	failing := w.failRemovesIn
	w.mu.Unlock()
	if failing != "" && key.Partition.Root == failing {
		return errors.New("the table is unreachable")
	}
	return w.Store.Remove(ctx, key, expected)
}

func (w *watchedKeyValues) removeAgain() {
	w.mu.Lock()
	w.failRemovesIn = ""
	w.mu.Unlock()
}

func (w *watchedKeyValues) lists() []listing {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.listed)
}

func statusesIn(t *testing.T, store variablestore.Store) []records.Entry {
	t.Helper()
	found, err := store.KeyValues.List(context.Background(), records.Partition{Tier: environment.TierProduction, Root: envsource.RootStatus})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func referenceCredentials(t *testing.T, store variablestore.Store, project, owner string) {
	t.Helper()
	for _, key := range []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"} {
		if _, err := store.SetReference(context.Background(), scopeOf(project), tierWide("", key), variablestore.Target{Project: owner, Cell: variablestore.Cell{Key: key}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestForgettingAProjectForgetsTheStatusItRegisteredUnderEvenAfterItsCredentialMoved(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	fake.put("/", fakeSecret{id: "s1", key: "K", value: "v", version: 1})
	setCredentials(t, store, "shared", "client-id", "client-secret")
	referenceCredentials(t, store, "shop", "shared")
	register(t, store, infisicalRegistration("shop", host, universal, ""))
	ctx := context.Background()
	if _, err := sync.CopyProject(ctx, infisicalRegistration("shop", host, universal, "")); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"} {
		if _, err := store.Delete(ctx, scopeOf("shop"), tierWide("", key), nil); err != nil {
			t.Fatal(err)
		}
	}
	setCredentials(t, store, "shop", "client-id", "client-secret")
	if err := envsource.ForgetProject(ctx, store, environment.TierProduction, "shop"); err != nil {
		t.Fatal(err)
	}
	if left := statusesIn(t, store); len(left) != 0 {
		t.Fatalf("status records left after the project was forgotten: %v", left)
	}
}

func TestRegisteringAnotherEnvSourceForgetsTheStatusOfTheOneBefore(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	fake.put("/", fakeSecret{id: "s1", key: "K", value: "v", version: 1})
	_, elsewhere := newFakeInfisical(t)
	before := infisicalRegistration("shop", host, cloudIdentity, "")
	register(t, store, before)
	if _, err := sync.CopyProject(context.Background(), before); err != nil {
		t.Fatal(err)
	}

	register(t, store, infisicalRegistration("shop", elsewhere.URL, cloudIdentity, ""))
	left := statusesIn(t, store)
	if len(left) != 1 || !slices.Equal(left[0].Key.Path[1:], []string{"projects", "shop"}) {
		t.Fatalf("status records = %v, want only shop's place under the env source it registered now", left)
	}
}

func TestAScheduledSyncMovesAProjectWhoseCredentialMovedToItsNewStatus(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	fake.put("/", fakeSecret{id: "s1", key: "K", value: "v", version: 1})
	setCredentials(t, store, "shared", "client-id", "client-secret")
	referenceCredentials(t, store, "shop", "shared")
	register(t, store, infisicalRegistration("shop", host, universal, ""))
	ctx := context.Background()
	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"} {
		if _, err := store.Delete(ctx, scopeOf("shop"), tierWide("", key), nil); err != nil {
			t.Fatal(err)
		}
	}
	setCredentials(t, store, "shop", "client-id", "client-secret")
	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}
	if err := envsource.ForgetProject(ctx, store, environment.TierProduction, "shop"); err != nil {
		t.Fatal(err)
	}
	if left := statusesIn(t, store); len(left) != 0 {
		t.Fatalf("status records left after the project was forgotten: %v", left)
	}
}

func TestForgettingAProjectReadsNoOtherProjectsRegistration(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	watched := &watchedKeyValues{Store: store.KeyValues}
	store.KeyValues = watched
	for _, project := range []string{"shop", "admin", "blog"} {
		register(t, store, infisicalRegistration(project, "https://infisical.example.com", cloudIdentity, ""))
	}

	if err := envsource.ForgetProject(context.Background(), store, environment.TierProduction, "shop"); err != nil {
		t.Fatal(err)
	}
	for _, under := range watched.lists() {
		if len(under.in.Path) == 0 && len(under.under) == 0 {
			t.Errorf("ForgetProject() listed everything in %s, want it to reach only what names shop or its status", under.in)
		}
	}
}

func TestADedupeKeyWhoseCredentialCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	setCredentials(t, store, "shop", "client-id", "client-secret")
	store.KeyValues = &watchedKeyValues{Store: store.KeyValues, failIn: variablestore.RootValues}
	descriptor := infisicalRegistration("shop", "https://infisical.example.com", universal, "").Descriptor
	if key, err := envsource.DedupeKey(context.Background(), store, scope, descriptor); err == nil {
		t.Fatalf("DedupeKey() over an unreadable store = %q, want the failure rather than a key naming the credential unset", key)
	}
}

func TestASyncUnderWayWhenItsProjectIsForgottenLeavesNoStatusBehind(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	fake.put("/", fakeSecret{id: "s1", key: "K", value: "v", version: 1})
	registration := infisicalRegistration("shop", host, cloudIdentity, "")
	register(t, store, registration)
	ctx := context.Background()
	if err := envsource.ForgetProject(ctx, store, environment.TierProduction, "shop"); err != nil {
		t.Fatal(err)
	}

	if _, err := sync.CopyProject(ctx, registration); err != nil {
		t.Fatal(err)
	}
	if left := statusesIn(t, store); len(left) != 0 {
		t.Fatalf("status records left by a sync that read the registration before its project was forgotten: %v", left)
	}
}

func TestForgettingAProjectAgainAfterItFailedPartWayLeavesNoStatusBehind(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	fake.put("/", fakeSecret{id: "s1", key: "K", value: "v", version: 1})
	registration := infisicalRegistration("shop", host, cloudIdentity, "")
	register(t, store, registration)
	ctx := context.Background()
	if _, err := sync.CopyProject(ctx, registration); err != nil {
		t.Fatal(err)
	}
	watched := &watchedKeyValues{Store: store.KeyValues, failRemovesIn: envsource.RootStatus}
	store.KeyValues = watched

	if err := envsource.ForgetProject(ctx, store, environment.TierProduction, "shop"); err == nil {
		t.Fatal("ForgetProject() whose status could not be removed = nil, want the failure")
	}
	watched.removeAgain()
	if err := envsource.ForgetProject(ctx, store, environment.TierProduction, "shop"); err != nil {
		t.Fatal(err)
	}
	if left := statusesIn(t, store); len(left) != 0 {
		t.Fatalf("status records left after the removal that failed part way was run again: %v", left)
	}
}

func TestAStatusRewrittenUnderEveryAttemptToRecordItIsAnError(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	fake.put("/", fakeSecret{id: "s1", key: "K", value: "v", version: 1})
	registration := infisicalRegistration("shop", host, cloudIdentity, "")
	register(t, store, registration)
	sync.Store.KeyValues = &watchedKeyValues{Store: store.KeyValues, staleIn: envsource.RootStatus}

	if _, err := sync.CopyProject(context.Background(), registration); err == nil {
		t.Fatal("CopyProject() whose status was rewritten under every attempt = nil, want the lost status reported")
	}
}

const racingWriters = 6

type racedWrites struct {
	records.Store

	mu   sync.Mutex
	root records.Root
	left int
}

func (r *racedWrites) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
	r.mu.Lock()
	raced := entry.Key.Partition.Root == r.root && r.left > 0
	if raced {
		r.left--
	}
	r.mu.Unlock()
	if raced {
		return "", records.ErrStale
	}
	return r.Store.Write(ctx, entry)
}

func TestAStatusSixWritersRaceToTheEntryIsStillRecorded(t *testing.T) {
	t.Parallel()
	sync, store, infisical, host, _ := syncFixture(t)
	infisical.put("/", fakeSecret{id: "s1", key: "K", value: "v", version: 1})
	registration := infisicalRegistration("shop", host, cloudIdentity, "")
	register(t, store, registration)
	sync.Store.KeyValues = &racedWrites{Store: store.KeyValues, root: envsource.RootStatus, left: racingWriters}

	if _, err := sync.CopyProject(context.Background(), registration); err != nil {
		t.Fatalf("CopyProject() whose status %d writers raced = %v, want the sync's outcome recorded", racingWriters, err)
	}
}
