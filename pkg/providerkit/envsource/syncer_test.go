package envsource_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func infisicalRegistration(project string, folders []string, auth envsource.InfisicalAuth) envsource.Registration {
	return envsource.Registration{
		Project: project,
		Descriptor: envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{
			Project: "p-1", Environment: "prod", Path: "/", Host: "https://infisical.example.com", Auth: auth, Write: envsource.WriteNever,
		}},
		Folders: folders,
	}
}

var awsIdentity = envsource.InfisicalAuth{Method: envsource.AuthAWS, IdentityID: "identity-1"}

type opened struct {
	sources map[string]*stubSource
	opens   int
	creds   []envsource.Credential
}

func (o *opened) open(_ context.Context, descriptor envsource.Descriptor, credential envsource.Credential) (envsource.Source, error) {
	o.opens++
	o.creds = append(o.creds, credential)
	return o.sources[descriptor.Infisical.Environment], nil
}

func syncerFixture(t *testing.T, source *stubSource) (*envsource.Syncer, values.Store, *opened, *clock) {
	t.Helper()
	store := values.Store{Records: fake.NewRecords(), Sealer: fake.NewSealer()}
	at := &clock{at: time.Unix(1_800_000_000, 0)}
	sources := &opened{sources: map[string]*stubSource{"prod": source}}
	return &envsource.Syncer{
		Store: store,
		Class: ports.ClassProduction,
		Open:  sources.open,
		Now:   at.now,
	}, store, sources, at
}

func scopeOf(project string) values.Scope {
	return values.Scope{Project: project, Class: ports.ClassProduction}
}

func TestProjectsSharingOneSourceCoordinateCostOneReadAPoll(t *testing.T) {
	source := &stubSource{id: "infisical:p-1/prod", held: map[values.Cell]envsource.Resolved{
		{Key: "SHARED"}:               {Value: []byte("root"), Version: "s1@1"},
		{Folder: "/web", Key: "WEB"}:  {Value: []byte("web"), Version: "s2@1"},
		{Folder: "/api", Key: "API"}:  {Value: []byte("api"), Version: "s3@1"},
		{Folder: "/jobs", Key: "JOB"}: {Value: []byte("job"), Version: "s4@1"},
	}}
	syncer, store, sources, _ := syncerFixture(t, source)
	ctx := context.Background()
	for _, registered := range []envsource.Registration{
		infisicalRegistration("shop", []string{"", "/web"}, awsIdentity),
		infisicalRegistration("admin", []string{"", "/api"}, awsIdentity),
	} {
		if err := envsource.Register(ctx, store.Records, ports.ClassProduction, registered); err != nil {
			t.Fatal(err)
		}
	}

	if err := syncer.Poll(ctx); err != nil {
		t.Fatalf("Poll() = %v", err)
	}
	if len(source.resolved) != 1 {
		t.Fatalf("resolved %d times for one shared coordinate, want once", len(source.resolved))
	}
	if folders := source.resolved[0]; !slices.Equal(folders, []string{"", "/api", "/web"}) {
		t.Fatalf("resolved folders = %v, want every registered folder once", folders)
	}
	if held := reveal(t, store, scopeOf("shop"), classWide("/web", "WEB")); held.Plaintext != "web" {
		t.Fatalf("shop /web WEB = %q", held.Plaintext)
	}
	if held := reveal(t, store, scopeOf("admin"), classWide("", "SHARED")); held.Plaintext != "root" {
		t.Fatalf("admin SHARED = %q", held.Plaintext)
	}
	if _, err := store.Get(ctx, scopeOf("shop"), classWide("/api", "API"), false); !errors.Is(err, values.ErrNotFound) {
		t.Fatalf("shop got a folder it never registered: %v", err)
	}

	if err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if sources.opens != 1 {
		t.Fatalf("opened the source %d times across two polls, want its sign-in kept", sources.opens)
	}
}

func TestAFailingSourceKeepsItsValuesAndIsRetriedAfterABackoff(t *testing.T) {
	source := &stubSource{id: "infisical:p-1/prod", held: map[values.Cell]envsource.Resolved{{Key: "K"}: {Value: []byte("v"), Version: "s1@1"}}}
	syncer, store, _, at := syncerFixture(t, source)
	ctx := context.Background()
	registered := infisicalRegistration("shop", []string{""}, awsIdentity)
	if err := envsource.Register(ctx, store.Records, ports.ClassProduction, registered); err != nil {
		t.Fatal(err)
	}
	if err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	synced := at.at

	source.err = errors.New("Infisical answered 503")
	at.at = at.at.Add(time.Minute)
	if err := syncer.Poll(ctx); err != nil {
		t.Fatalf("Poll() over a failing source = %v, want the failure kept to its status", err)
	}
	status, err := envsource.StatusOf(ctx, store, ports.ClassProduction, registered)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastError == "" || !strings.Contains(status.LastError, "503") || status.LastSuccessAt != synced.Unix() || status.LastAttemptAt != at.at.Unix() {
		t.Fatalf("status = %+v, want the failure recorded beside the last success", status)
	}
	if held := reveal(t, store, scopeOf("shop"), classWide("", "K")); held.Plaintext != "v" {
		t.Fatal("a failing source dropped the last synced value")
	}

	attempts := len(source.resolved)
	at.at = at.at.Add(10 * time.Second)
	if err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(source.resolved) != attempts {
		t.Fatal("a source that just failed was read again before its backoff elapsed")
	}

	source.err = nil
	at.at = at.at.Add(10 * time.Minute)
	if err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	status, err = envsource.StatusOf(ctx, store, ports.ClassProduction, registered)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastError != "" || status.LastSuccessAt != at.at.Unix() {
		t.Fatalf("status after recovery = %+v", status)
	}
}

func TestACredentialReadsFromOcelsOwnStoreAndNeverFromTheSource(t *testing.T) {
	source := &stubSource{id: "infisical:p-1/prod", held: map[values.Cell]envsource.Resolved{{Key: "K"}: {Value: []byte("v"), Version: "s1@1"}}}
	syncer, store, sources, _ := syncerFixture(t, source)
	ctx := context.Background()
	universal := envsource.InfisicalAuth{Method: envsource.AuthUniversal, ClientIDVar: "INFISICAL_CLIENT_ID", ClientSecretVar: "INFISICAL_CLIENT_SECRET"}
	registered := infisicalRegistration("shop", []string{""}, universal)
	if err := envsource.Register(ctx, store.Records, ports.ClassProduction, registered); err != nil {
		t.Fatal(err)
	}

	if err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := envsource.StatusOf(ctx, store, ports.ClassProduction, registered)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.LastError, "INFISICAL_CLIENT_ID") || sources.opens != 0 {
		t.Fatalf("status = %+v, opens = %d: want an unset credential named and the source never opened", status, sources.opens)
	}

	for key, value := range map[string]string{"INFISICAL_CLIENT_ID": "id", "INFISICAL_CLIENT_SECRET": "secret"} {
		if _, err := store.Set(ctx, scopeOf("shop"), classWide("", key), value, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := envsource.Open(ctx, store, scopeOf("shop"), registered.Descriptor, envsource.Target{}); err != nil {
		t.Fatalf("Open() with its credential set = %v", err)
	}

	if _, err := store.Mirror(ctx, scopeOf("shop"), classWide("", "INFISICAL_CLIENT_SECRET"), "cached", values.Provenance{EnvSource: "infisical:p-1/prod", Version: "s9@1"}, 1); err != nil {
		t.Fatal(err)
	}
	_, err = envsource.Open(ctx, store, scopeOf("shop"), registered.Descriptor, envsource.Target{})
	if err == nil || !strings.Contains(err.Error(), "INFISICAL_CLIENT_SECRET") || !strings.Contains(err.Error(), "infisical:p-1/prod") {
		t.Fatalf("Open() with a credential the source itself wrote = %v, want the cycle refused", err)
	}
}

func TestACredentialBorrowedFromAProjectThatReadsItFromItsOwnSourceIsRefused(t *testing.T) {
	store, _ := storeFixture()
	ctx := context.Background()
	universal := envsource.InfisicalAuth{Method: envsource.AuthUniversal, ClientIDVar: "ID", ClientSecretVar: "SECRET"}
	for _, key := range []string{"ID", "SECRET", "OTHER"} {
		if _, err := store.Set(ctx, scopeOf("shared"), classWide("", key), "x", nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"ID", "SECRET"} {
		if _, err := store.SetReference(ctx, scopeOf("shop"), classWide("", key), values.Target{Project: "shared", Cell: values.Cell{Key: key}}); err != nil {
			t.Fatal(err)
		}
	}
	descriptor := infisicalRegistration("shop", []string{""}, universal).Descriptor
	if _, err := envsource.Open(ctx, store, scopeOf("shop"), descriptor, envsource.Target{}); err != nil {
		t.Fatalf("Open() borrowing from a project on ocel's own store = %v", err)
	}

	if err := envsource.Register(ctx, store.Records, ports.ClassProduction, infisicalRegistration("shared", []string{""}, universal)); err != nil {
		t.Fatal(err)
	}
	if _, err := envsource.Open(ctx, store, scopeOf("shop"), descriptor, envsource.Target{}); err != nil {
		t.Fatalf("Open() borrowing the credential cells of a project on a source = %v, want them builtin-owned", err)
	}

	if _, err := store.SetReference(ctx, scopeOf("shop"), classWide("", "SECRET"), values.Target{Project: "shared", Cell: values.Cell{Key: "OTHER"}}); err != nil {
		t.Fatal(err)
	}
	_, err := envsource.Open(ctx, store, scopeOf("shop"), descriptor, envsource.Target{})
	if err == nil || !strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "shared") {
		t.Fatalf("Open() borrowing a cell a source owns = %v, want it refused", err)
	}
}

func TestUnregisteringStopsTheSync(t *testing.T) {
	source := &stubSource{id: "infisical:p-1/prod", held: map[values.Cell]envsource.Resolved{{Key: "K"}: {Value: []byte("v"), Version: "s1@1"}}}
	syncer, store, _, _ := syncerFixture(t, source)
	ctx := context.Background()
	if err := envsource.Register(ctx, store.Records, ports.ClassProduction, infisicalRegistration("shop", []string{""}, awsIdentity)); err != nil {
		t.Fatal(err)
	}
	if err := envsource.Unregister(ctx, store.Records, ports.ClassProduction, "shop"); err != nil {
		t.Fatal(err)
	}
	if err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(source.resolved) != 0 {
		t.Fatal("an unregistered project was synced")
	}
	if _, found, err := envsource.Registered(ctx, store.Records, ports.ClassProduction, "shop"); err != nil || found {
		t.Fatalf("Registered() = %v, %v, want nothing", found, err)
	}
}
