package envsource_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(by)
}

func syncFixture(t *testing.T) (*envsource.Sync, envvars.Store, *fakeInfisical, string, *clock) {
	t.Helper()
	fake, server := newFakeInfisical(t)
	store, _ := storeFixture()
	at := &clock{at: time.Unix(1_800_000_000, 0)}
	return &envsource.Sync{
		Store: store,
		Class: edge.ClassProduction,
		Login: envsource.Login{ProveIdentity: proveBySignedRequest, Client: server.Client()},
		Now:   at.now,
	}, store, fake, server.URL, at
}

func register(t *testing.T, store envvars.Store, registration envsource.Registration) {
	t.Helper()
	if err := envsource.Register(context.Background(), store.Records, edge.ClassProduction, registration); err != nil {
		t.Fatal(err)
	}
}

func statusOf(t *testing.T, store envvars.Store, registration envsource.Registration) envsource.Status {
	t.Helper()
	status, err := envsource.StatusOf(context.Background(), store, edge.ClassProduction, registration)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestProjectsSharingOneSourceCoordinateCostOneReadAPoll(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	fake.put("/", fakeSecret{id: "s1", key: "SHARED", value: "root", version: 1})
	fake.put("/web", fakeSecret{id: "s2", key: "WEB", value: "web", version: 1})
	fake.put("/api", fakeSecret{id: "s3", key: "API", value: "api", version: 1})
	fake.put("/jobs", fakeSecret{id: "s4", key: "JOB", value: "job", version: 1})
	shop := infisicalRegistration("shop", host, cloudIdentity, "", "/web")
	register(t, store, shop)
	register(t, store, infisicalRegistration("admin", host, cloudIdentity, "", "/api"))
	ctx := context.Background()

	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatalf("CopyScheduled() = %v", err)
	}
	if listed := fake.listedPaths(); !slices.Equal(listed, []string{"/", "/api", "/web"}) {
		t.Fatalf("listed %v, want every registered folder once for the one shared coordinate", listed)
	}
	if web := reveal(t, store, scopeOf("shop"), classWide("/web", "WEB")); web.Plaintext != "web" {
		t.Fatalf("shop /web WEB = %q", web.Plaintext)
	}
	if shared := reveal(t, store, scopeOf("admin"), classWide("", "SHARED")); shared.Plaintext != "root" {
		t.Fatalf("admin SHARED = %q", shared.Plaintext)
	}
	if _, err := store.Get(ctx, scopeOf("shop"), classWide("/api", "API"), false); !errors.Is(err, envvars.ErrNotFound) {
		t.Fatalf("shop got a folder it never registered: %v", err)
	}
	status := statusOf(t, store, shop)
	if status.EnvSource != "infisical:p-1/prod" || status.LastError != "" || status.LastSuccessAt.IsZero() || !strings.HasPrefix(status.URLs["/web"], host+"/organizations/org-1/") {
		t.Fatalf("status = %+v, want a success with each folder's URL", status)
	}

	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}
	if logins := fake.loginCount(); logins != 1 {
		t.Fatalf("logged in %d times across two polls, want the session kept", logins)
	}
}

func TestAFailingSourceKeepsItsValuesAndIsReadAgainOnlyAfterABackoff(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, at := syncFixture(t)
	fake.put("/", fakeSecret{id: "s1", key: "K", value: "v", version: 1})
	registration := infisicalRegistration("shop", host, cloudIdentity, "")
	register(t, store, registration)
	ctx := context.Background()
	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}
	synced := at.now()

	fake.set(func(f *fakeInfisical) { f.refuseList = true })
	at.advance(time.Minute)
	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatalf("CopyScheduled() over a failing source = %v, want the failure kept to its status", err)
	}
	status := statusOf(t, store, registration)
	if !strings.Contains(status.LastError, "403") || !status.LastSuccessAt.Equal(synced) || !status.LastAttemptAt.Equal(at.now()) || !status.RetryAt.After(at.now()) {
		t.Fatalf("status = %+v, want the failure recorded beside the last success, and a retry scheduled", status)
	}
	if k := reveal(t, store, scopeOf("shop"), classWide("", "K")); k.Plaintext != "v" {
		t.Fatal("a failing source dropped the last copied value")
	}

	reads := len(fake.listedPaths())
	at.advance(10 * time.Second)
	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fake.listedPaths()) != reads {
		t.Fatal("a source that just failed was read again before its backoff elapsed")
	}

	fake.set(func(f *fakeInfisical) { f.refuseList = false })
	at.advance(20 * time.Minute)
	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}
	if status := statusOf(t, store, registration); status.LastError != "" || !status.LastSuccessAt.Equal(at.now()) || !status.RetryAt.IsZero() {
		t.Fatalf("status after recovery = %+v", status)
	}
}

func TestAnUnsetCredentialIsRecordedAndTheSourceNeverLoggedInTo(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	registration := infisicalRegistration("shop", host, universal, "")
	register(t, store, registration)

	if err := sync.CopyScheduled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := statusOf(t, store, registration); !strings.Contains(status.LastError, "INFISICAL_CLIENT_ID") || fake.loginCount() != 0 {
		t.Fatalf("status = %+v, logins = %d: want the unset credential named and no login", status, fake.loginCount())
	}

	setCredentials(t, store, "shop", "client-id", "client-secret")
	fake.put("/", fakeSecret{id: "s1", key: "K", value: "v", version: 1})
	result, err := sync.CopyProject(context.Background(), registration)
	if err != nil || !slices.Equal(result.Written, []envvars.Cell{cell("", "K")}) {
		t.Fatalf("CopyProject() with the credential set = %+v, %v", result, err)
	}
}

func TestProjectReturnsTheFailureAndRecordsIt(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	fake.set(func(f *fakeInfisical) { f.refuseList = true })
	registration := infisicalRegistration("shop", host, cloudIdentity, "")
	register(t, store, registration)

	if _, err := sync.CopyProject(context.Background(), registration); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("CopyProject() = %v, want Infisical's refusal", err)
	}
	if status := statusOf(t, store, registration); !strings.Contains(status.LastError, "403") {
		t.Fatalf("status = %+v, want the failure recorded", status)
	}
}

func TestCopyingAProjectFromASourceAlreadyReadCopiesWhatItHoldsAndRecordsTheSuccess(t *testing.T) {
	t.Parallel()
	sync, store, _, _, _ := syncFixture(t)
	registration := envsource.Registration{Project: "shop", Descriptor: execDescriptor(envsource.FormatJSON, "vault"), Folders: []string{"", "/web"}}
	register(t, store, registration)
	source := envsource.NewFixed("exec", map[envvars.Cell]envsource.Value{cell("/web", "WEB"): value("w", "v1")})

	result, err := sync.CopyProjectFrom(context.Background(), registration, source)
	if err != nil || !slices.Equal(result.Written, []envvars.Cell{cell("/web", "WEB")}) {
		t.Fatalf("CopyProjectFrom() = %+v, %v", result, err)
	}
	if web := reveal(t, store, scopeOf("shop"), classWide("/web", "WEB")); web.Provenance.EnvSource != "exec" {
		t.Fatalf("WEB = %+v, want it named as exec's", web)
	}
	if status := statusOf(t, store, registration); status.EnvSource != "exec" || status.LastSuccessAt.IsZero() {
		t.Fatalf("status = %+v, want the success recorded", status)
	}
}

func TestACopyOfScheduledEnvSourcesSkipsOneThatIsNotScheduled(t *testing.T) {
	t.Parallel()
	sync, store, _, _, _ := syncFixture(t)
	registration := envsource.Registration{Project: "shop", Descriptor: execDescriptor(envsource.FormatJSON, "vault"), Folders: []string{""}}
	register(t, store, registration)
	if err := sync.CopyScheduled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := statusOf(t, store, registration); !status.LastAttemptAt.IsZero() {
		t.Fatalf("status = %+v, want an exec source never polled", status)
	}
}

func TestOpenGivesTheSourceAWriteGoesThrough(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	registration := infisicalRegistration("shop", host, cloudIdentity, "")
	registration.Descriptor.Infisical.Write = envsource.WriteMissing
	register(t, store, registration)

	source, err := sync.Open(context.Background(), registration)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Create(context.Background(), cell("", "NEW"), []byte("v"), "made by a deploy"); err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if created := fake.stored("/"); len(created) != 1 || created[0].key != "NEW" {
		t.Fatalf("stored = %+v", created)
	}
}

func TestUnregisteringStopsTheSync(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	register(t, store, infisicalRegistration("shop", host, cloudIdentity, ""))
	if err := envsource.Unregister(context.Background(), store.Records, edge.ClassProduction, "shop"); err != nil {
		t.Fatal(err)
	}
	if err := sync.CopyScheduled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if listed := fake.listedPaths(); len(listed) != 0 {
		t.Fatalf("an unregistered project was read: %v", listed)
	}
}

func TestForgettingAProjectKeepsTheStatusAnotherProjectShares(t *testing.T) {
	t.Parallel()
	sync, store, _, host, _ := syncFixture(t)
	shop := infisicalRegistration("shop", host, cloudIdentity, "")
	admin := infisicalRegistration("admin", host, cloudIdentity, "")
	register(t, store, shop)
	register(t, store, admin)
	ctx := context.Background()
	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}

	if err := envsource.ForgetProject(ctx, store, edge.ClassProduction, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, registered, _ := envsource.Registered(ctx, store.Records, edge.ClassProduction, "shop"); registered {
		t.Fatal("a forgotten project is still registered")
	}
	if status := statusOf(t, store, admin); status.LastSuccessAt.IsZero() {
		t.Fatal("forgetting shop dropped the status admin shares")
	}

	if err := envsource.ForgetProject(ctx, store, edge.ClassProduction, "admin"); err != nil {
		t.Fatal(err)
	}
	if status := statusOf(t, store, admin); !status.LastSuccessAt.IsZero() {
		t.Fatalf("status = %+v, want it forgotten with the last project reading it", status)
	}
	if err := envsource.ForgetProject(ctx, store, edge.ClassProduction, "never-registered"); err != nil {
		t.Fatalf("ForgetProject() of a project with no env source = %v", err)
	}
}

func TestCopyingEveryIntervalPollsUntilItsContextEnds(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	sync.Interval = time.Millisecond
	register(t, store, infisicalRegistration("shop", host, cloudIdentity, ""))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sync.CopyScheduledEveryInterval(ctx, func(err error) { t.Errorf("CopyScheduledEveryInterval() reported %v", err) })
		close(done)
	}()
	deadline := time.After(10 * time.Second)
	for len(fake.listedPaths()) < 2 {
		select {
		case <-deadline:
			t.Fatal("CopyScheduledEveryInterval() never polled twice")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
}
