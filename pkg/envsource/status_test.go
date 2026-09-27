package envsource_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type watchedRecords struct {
	records.Store

	mu         sync.Mutex
	listed     []records.Name
	staleUnder records.Name
	failUnder  records.Name
}

func (w *watchedRecords) List(ctx context.Context, under records.Name) ([]records.Record, error) {
	w.mu.Lock()
	w.listed = append(w.listed, under)
	w.mu.Unlock()
	return w.Store.List(ctx, under)
}

func (w *watchedRecords) Read(ctx context.Context, name records.Name) (records.Record, error) {
	if _, under := name.Under(w.failUnder); w.failUnder != nil && under {
		return records.Record{}, errors.New("the table is unreachable")
	}
	return w.Store.Read(ctx, name)
}

func (w *watchedRecords) Write(ctx context.Context, record records.Record) (records.Revision, error) {
	if _, under := record.Name.Under(w.staleUnder); w.staleUnder != nil && under {
		return "", records.ErrStale
	}
	return w.Store.Write(ctx, record)
}

func (w *watchedRecords) lists() []records.Name {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.listed)
}

func statusesIn(t *testing.T, store envvars.Store) []records.Record {
	t.Helper()
	found, err := store.Records.List(context.Background(), records.Name{records.RootEnvSourceStatus, string(edge.ClassProduction)})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func referenceCredentials(t *testing.T, store envvars.Store, project, owner string) {
	t.Helper()
	for _, key := range []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"} {
		if _, err := store.SetReference(context.Background(), scopeOf(project), classWide("", key), envvars.Target{Project: owner, Cell: envvars.Cell{Key: key}}); err != nil {
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
		if _, err := store.Delete(ctx, scopeOf("shop"), classWide("", key), nil); err != nil {
			t.Fatal(err)
		}
	}
	setCredentials(t, store, "shop", "client-id", "client-secret")
	if err := envsource.ForgetProject(ctx, store, edge.ClassProduction, "shop"); err != nil {
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
	if len(left) != 1 || !slices.Equal(left[0].Name[3:], records.Name{"projects", "shop"}) {
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
		if _, err := store.Delete(ctx, scopeOf("shop"), classWide("", key), nil); err != nil {
			t.Fatal(err)
		}
	}
	setCredentials(t, store, "shop", "client-id", "client-secret")
	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}
	if err := envsource.ForgetProject(ctx, store, edge.ClassProduction, "shop"); err != nil {
		t.Fatal(err)
	}
	if left := statusesIn(t, store); len(left) != 0 {
		t.Fatalf("status records left after the project was forgotten: %v", left)
	}
}

func TestForgettingAProjectReadsNoOtherProjectsRegistration(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	watched := &watchedRecords{Store: store.Records}
	store.Records = watched
	for _, project := range []string{"shop", "admin", "blog"} {
		register(t, store, infisicalRegistration(project, "https://infisical.example.com", cloudIdentity, ""))
	}

	if err := envsource.ForgetProject(context.Background(), store, edge.ClassProduction, "shop"); err != nil {
		t.Fatal(err)
	}
	for _, under := range watched.lists() {
		if len(under) <= 2 {
			t.Errorf("ForgetProject() listed everything under %s, want it to reach only what names shop or its status", under)
		}
	}
}

func TestADedupeKeyWhoseCredentialCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	setCredentials(t, store, "shop", "client-id", "client-secret")
	store.Records = &watchedRecords{Store: store.Records, failUnder: records.Name{records.RootValues}}
	descriptor := infisicalRegistration("shop", "https://infisical.example.com", universal, "").Descriptor
	if key, err := envsource.DedupeKey(context.Background(), store, scope, descriptor); err == nil {
		t.Fatalf("DedupeKey() over an unreadable store = %q, want the failure rather than a key naming the credential unset", key)
	}
}
