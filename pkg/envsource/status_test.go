package envsource_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

type listing struct {
	in    keyvalue.Partition
	under []string
}

type watchedKeyValues struct {
	keyvalue.Store

	mu            sync.Mutex
	listed        []listing
	staleIn       keyvalue.Root
	failIn        keyvalue.Root
	failRemovesIn keyvalue.Root
}

func (w *watchedKeyValues) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	w.mu.Lock()
	w.listed = append(w.listed, listing{in: in, under: under})
	w.mu.Unlock()
	return w.Store.List(ctx, in, under...)
}

func (w *watchedKeyValues) Read(ctx context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	if w.failIn != "" && key.Partition.Root == w.failIn {
		return keyvalue.Entry{}, errors.New("the table is unreachable")
	}
	return w.Store.Read(ctx, key)
}

func (w *watchedKeyValues) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	if w.staleIn != "" && entry.Key.Partition.Root == w.staleIn {
		return "", keyvalue.ErrStale
	}
	return w.Store.Write(ctx, entry)
}

func (w *watchedKeyValues) Remove(ctx context.Context, key keyvalue.Key, expected keyvalue.Revision) error {
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

func statusesIn(t *testing.T, store variablestore.Store) []keyvalue.Entry {
	t.Helper()
	found, err := store.KeyValues.List(context.Background(), keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootEnvSourceStatus})
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
	store.KeyValues = &watchedKeyValues{Store: store.KeyValues, failIn: keyvalue.RootValues}
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
	watched := &watchedKeyValues{Store: store.KeyValues, failRemovesIn: keyvalue.RootEnvSourceStatus}
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
	sync.Store.KeyValues = &watchedKeyValues{Store: store.KeyValues, staleIn: keyvalue.RootEnvSourceStatus}

	if _, err := sync.CopyProject(context.Background(), registration); err == nil {
		t.Fatal("CopyProject() whose status was rewritten under every attempt = nil, want the lost status reported")
	}
}
