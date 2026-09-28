package envsource_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
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
		Tier:  environment.TierProduction,
		Login: envsource.Login{ProveIdentity: proveBySignedRequest, Client: server.Client()},
		Now:   at.now,
	}, store, fake, server.URL, at
}

func register(t *testing.T, store envvars.Store, registration envsource.Registration) {
	t.Helper()
	if _, err := envsource.Register(context.Background(), store, environment.TierProduction, registration); err != nil {
		t.Fatal(err)
	}
}

func statusOf(t *testing.T, store envvars.Store, registration envsource.Registration) envsource.Status {
	t.Helper()
	status, err := envsource.StatusOf(context.Background(), store, environment.TierProduction, registration)
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
	if web := reveal(t, store, scopeOf("shop"), tierWide("/web", "WEB")); web.Plaintext != "web" {
		t.Fatalf("shop /web WEB = %q", web.Plaintext)
	}
	if shared := reveal(t, store, scopeOf("admin"), tierWide("", "SHARED")); shared.Plaintext != "root" {
		t.Fatalf("admin SHARED = %q", shared.Plaintext)
	}
	if _, err := store.Get(ctx, scopeOf("shop"), tierWide("/api", "API"), false); !errors.Is(err, envvars.ErrNotFound) {
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
	if k := reveal(t, store, scopeOf("shop"), tierWide("", "K")); k.Plaintext != "v" {
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

func TestAnEnvSourceWhoseRootIsGoneKeepsEveryValueItCopied(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	fake.put("/", fakeSecret{id: "s1", key: "DATABASE_URL", value: "postgres://prod", version: 1})
	fake.put("/web", fakeSecret{id: "s2", key: "API_KEY", value: "web-key", version: 1})
	registration := infisicalRegistration("shop", host, cloudIdentity, "", "/web")
	register(t, store, registration)
	ctx := context.Background()
	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}

	fake.set(func(f *fakeInfisical) { f.folders = map[string]bool{} })
	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}
	for _, at := range []envvars.Coordinate{tierWide("", "DATABASE_URL"), tierWide("/web", "API_KEY")} {
		if _, err := store.Get(ctx, scopeOf("shop"), at, false); err != nil {
			t.Errorf("%s after its env source's root went missing = %v, want the copied value kept", at, err)
		}
	}
	if status := statusOf(t, store, registration); !strings.Contains(status.LastError, "404") {
		t.Fatalf("status = %+v, want the missing root recorded as a failure", status)
	}
}

func onceWithin(t *testing.T, sync *envsource.Sync, limit time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- sync.CopyScheduled(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CopyScheduled() = %v", err)
		}
	case <-time.After(limit):
		t.Fatalf("CopyScheduled() ran past %s, the time a scheduled sync has before the platform stops it", limit)
	}
}

func TestASlowEnvSourceYieldsTheRestOfTheSyncToTheOthers(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	sync.Interval = 600 * time.Millisecond
	slow, slowServer := newFakeInfisical(t)
	slow.hangList.Store(true)
	fake.put("/", fakeSecret{id: "s1", key: "K", value: "v", version: 1})
	stuck := infisicalRegistration("admin", slowServer.URL, cloudIdentity, "")
	register(t, store, stuck)
	register(t, store, infisicalRegistration("shop", host, cloudIdentity, ""))

	onceWithin(t, sync, 5*time.Second)
	if k := reveal(t, store, scopeOf("shop"), tierWide("", "K")); k.Plaintext != "v" {
		t.Fatalf("K = %q, want the env source after the slow one copied within the same sync", k.Plaintext)
	}
	if status := statusOf(t, store, stuck); status.LastError == "" || status.LastAttemptAt.IsZero() {
		t.Fatalf("status = %+v, want the slow env source's overrun recorded", status)
	}
}

func TestTheEnvSourceTriedLongestAgoIsReadFirst(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, at := syncFixture(t)
	fake.put("/api")
	fake.put("/web")
	admin := infisicalRegistration("admin", host, envsource.InfisicalAuth{Method: envsource.AuthIdentity, IdentityID: "identity-2"}, "/api")
	register(t, store, admin)
	register(t, store, infisicalRegistration("shop", host, cloudIdentity, "/web"))
	ctx := context.Background()
	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}
	at.advance(time.Minute)
	if _, err := sync.CopyProject(ctx, admin); err != nil {
		t.Fatal(err)
	}
	at.advance(time.Minute)
	before := len(fake.listedPaths())

	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}
	if listed := fake.listedPaths()[before:]; !slices.Equal(listed, []string{"/web", "/api"}) {
		t.Fatalf("listed %v, want shop, tried longest ago, read before admin", listed)
	}
}

func TestAThrottleLongerThanTheSyncIsWaitedOutInTheStatusNotInTheSync(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, at := syncFixture(t)
	fake.set(func(f *fakeInfisical) {
		f.throttle = 100
		f.retryAfter = "3600"
	})
	registration := infisicalRegistration("shop", host, cloudIdentity, "")
	register(t, store, registration)

	onceWithin(t, sync, 5*time.Second)
	status := statusOf(t, store, registration)
	if !status.RetryAt.After(at.now().Add(59 * time.Minute)) {
		t.Fatalf("status = %+v, want the retry put off for as long as Infisical asked", status)
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

func TestEachSyncLogsInWithTheCredentialStoredNow(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, at := syncFixture(t)
	fake.put("/", fakeSecret{id: "s1", key: "K", value: "v", version: 1})
	setCredentials(t, store, "shop", "client-id", "client-secret")
	registration := infisicalRegistration("shop", host, universal, "")
	register(t, store, registration)
	ctx := context.Background()
	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}

	fake.set(func(f *fakeInfisical) { f.secret = "rotated" })
	setCredentials(t, store, "shop", "client-id", "rotated")
	at.advance(time.Minute)
	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}
	var secrets []string
	fake.set(func(f *fakeInfisical) {
		for _, body := range f.loginBodies {
			secrets = append(secrets, body["clientSecret"])
		}
	})
	if !slices.Equal(secrets, []string{"client-secret", "rotated"}) {
		t.Fatalf("logged in with %v, want the rotated secret used by the sync after it was stored", secrets)
	}

	if _, err := store.Delete(ctx, scopeOf("shop"), tierWide("", "INFISICAL_CLIENT_SECRET"), nil); err != nil {
		t.Fatal(err)
	}
	reads := len(fake.listedPaths())
	at.advance(time.Minute)
	if err := sync.CopyScheduled(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fake.listedPaths()) != reads {
		t.Fatal("a sync read the env source with a credential that was removed from ocel's store")
	}
	if status := statusOf(t, store, registration); !strings.Contains(status.LastError, "INFISICAL_CLIENT_SECRET") {
		t.Fatalf("status = %+v, want the removed credential named", status)
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
	if web := reveal(t, store, scopeOf("shop"), tierWide("/web", "WEB")); web.Provenance.EnvSource != "exec" {
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

func TestForgettingAProjectStopsItsSync(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	register(t, store, infisicalRegistration("shop", host, cloudIdentity, ""))
	if err := envsource.ForgetProject(context.Background(), store, environment.TierProduction, "shop"); err != nil {
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

	if err := envsource.ForgetProject(ctx, store, environment.TierProduction, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, registered, _ := envsource.Registered(ctx, store.KeyValues, environment.TierProduction, "shop"); registered {
		t.Fatal("a forgotten project is still registered")
	}
	if status := statusOf(t, store, admin); status.LastSuccessAt.IsZero() {
		t.Fatal("forgetting shop dropped the status admin shares")
	}

	if err := envsource.ForgetProject(ctx, store, environment.TierProduction, "admin"); err != nil {
		t.Fatal(err)
	}
	if status := statusOf(t, store, admin); !status.LastSuccessAt.IsZero() {
		t.Fatalf("status = %+v, want it forgotten with the last project reading it", status)
	}
	if err := envsource.ForgetProject(ctx, store, environment.TierProduction, "never-registered"); err != nil {
		t.Fatalf("ForgetProject() of a project with no env source = %v", err)
	}
}

func TestCopyingEveryIntervalPollsUntilItsContextEnds(t *testing.T) {
	t.Parallel()
	sync, store, fake, host, _ := syncFixture(t)
	sync.Interval = 100 * time.Millisecond
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
