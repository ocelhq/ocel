package fake_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestRecordsWriteIsACompareAndSet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewRecords()
	name := records.Name{"edgestacks", "production", "shop"}

	first, err := store.Write(ctx, records.Record{Name: name, Bytes: []byte("one")})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if _, err := store.Write(ctx, records.Record{Name: name, Bytes: []byte("two")}); !errors.Is(err, records.ErrStale) {
		t.Fatalf("Write() over an existing record without its revision = %v, want ErrStale", err)
	}

	second, err := store.Write(ctx, records.Record{Name: name, Bytes: []byte("two"), Revision: first})
	if err != nil {
		t.Fatalf("Write() at the revision it was read at: error = %v", err)
	}
	if second == first {
		t.Error("Write() reused the revision, so a lost update would go unnoticed")
	}

	if _, err := store.Write(ctx, records.Record{Name: name, Bytes: []byte("three"), Revision: first}); !errors.Is(err, records.ErrStale) {
		t.Fatalf("Write() at a revision that moved = %v, want ErrStale", err)
	}
}

func TestRecordsWriteRefusesARevisionForARecordThatIsNotThere(t *testing.T) {
	t.Parallel()

	store := fake.NewRecords()
	_, err := store.Write(context.Background(), records.Record{
		Name:     records.Name{"schema"},
		Bytes:    []byte("{}"),
		Revision: "1",
	})
	if !errors.Is(err, records.ErrStale) {
		t.Fatalf("Write() = %v, want ErrStale", err)
	}
}

func TestRecordsReadAndRemove(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewRecords()
	name := records.Name{"schema"}

	if _, err := store.Read(ctx, name); !errors.Is(err, records.ErrNotFound) {
		t.Fatalf("Read() of an absent record = %v, want ErrNotFound", err)
	}

	revision, err := store.Write(ctx, records.Record{Name: name, Bytes: []byte("{}")})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	read, err := store.Read(ctx, name)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if string(read.Bytes) != "{}" || read.Revision != revision {
		t.Errorf("Read() = %+v, want the bytes written at revision %q", read, revision)
	}

	if err := store.Remove(ctx, name, "not-the-revision"); !errors.Is(err, records.ErrStale) {
		t.Fatalf("Remove() at the wrong revision = %v, want ErrStale", err)
	}
	if err := store.Remove(ctx, name, revision); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := store.Read(ctx, name); !errors.Is(err, records.ErrNotFound) {
		t.Fatalf("Read() after Remove = %v, want ErrNotFound", err)
	}
}

func TestRecordsListIsScopedToThePrefix(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewRecords()
	for _, slug := range []string{"shop", "blog"} {
		if _, err := store.Write(ctx, records.Record{Name: records.Name{"projects", slug}}); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if _, err := store.Write(ctx, records.Record{Name: records.Name{"schema"}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	found, err := store.List(ctx, records.Name{"projects"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("List() returned %d records, want the two under projects/", len(found))
	}
	if found[0].Name.String() != "projects/blog" || found[1].Name.String() != "projects/shop" {
		t.Errorf("List() = %q, %q, want them sorted by name", found[0].Name, found[1].Name)
	}
}

func TestSealerBindsAValueToItsCoordinate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cipher := fake.NewCipher()
	at := records.SealScope{Project: "shop", Class: edge.ClassProduction, Env: "production", Name: "DATABASE_URL"}

	sealed, err := cipher.Seal(ctx, at, []byte("postgres://"))
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if bytes.Contains(sealed, []byte("postgres://")) {
		t.Fatal("Seal() left the plaintext in the sealed bytes")
	}

	opened, err := cipher.Open(ctx, at, sealed)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if string(opened) != "postgres://" {
		t.Errorf("Open() = %q", opened)
	}

	elsewhere := at
	elsewhere.Name = "OTHER_URL"
	if _, err := cipher.Open(ctx, elsewhere, sealed); err == nil {
		t.Fatal("Open() at another coordinate succeeded, want the coordinate to bind the value")
	}
}

func TestArtifactsRemovePrefixLeavesTheRest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	artifacts := fake.NewArtifacts()
	kept := provider.ArtifactRef{Class: edge.ClassProduction, Bucket: provider.StoreFunctions, Key: "other/app.zip"}
	removed := provider.ArtifactRef{Class: edge.ClassProduction, Bucket: provider.StoreAssets, Key: "releases/r1/app.zip"}

	for _, ref := range []provider.ArtifactRef{kept, removed} {
		if err := artifacts.Put(ctx, ref, bytes.NewReader([]byte("body"))); err != nil {
			t.Fatalf("Put() error = %v", err)
		}
	}

	if err := artifacts.RemovePrefix(ctx, edge.ClassProduction, "releases/", nil); err != nil {
		t.Fatalf("RemovePrefix() error = %v", err)
	}
	if _, err := artifacts.Open(ctx, removed); err == nil {
		t.Error("Open() found an artifact under the removed prefix")
	}
	if _, err := artifacts.Open(ctx, kept); err != nil {
		t.Errorf("Open() of an artifact outside the prefix: error = %v", err)
	}
}

func TestNewRefusesOptionsTheReferenceProviderDoesNotAccept(t *testing.T) {
	t.Parallel()

	if _, err := fake.New(context.Background(), provider.Settings{Options: provider.Options{"regoin": "typo"}}); err == nil {
		t.Fatal("New() accepted an option it does not know")
	}
	if _, err := fake.New(context.Background(), provider.Settings{Options: provider.Options{"region": "nowhere"}}); err != nil {
		t.Fatalf("New() error = %v", err)
	}
}

func TestTheReferenceProviderIsReachedThroughThePrimitiveItsAppsComputeNames(t *testing.T) {
	t.Parallel()

	p := fake.NewProvider(fake.Options{})
	stacks := resources.Stacks(p.Records(), p.Artifacts(), p.ResourceHooks())
	ref := provider.StackRef{
		Project: "shop",
		Class:   edge.ClassProduction,
		Name:    naming.AppStack("prod", "web", naming.NewRelease("d1", "f1")),
	}

	served, err := stacks.Provision(context.Background(), provider.StackPlan{
		Ref:  ref,
		Kind: provider.StackApp,
		App: &provider.AppPlan{
			App:       "web",
			Compute:   provider.ComputeServerless,
			Functions: []provider.FunctionSpec{{Name: "api"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() of a serverless app = %v", err)
	}
	if len(served.Functions) != 1 || len(served.Containers) != 0 {
		t.Fatalf("Provision() of a serverless app = %+v, want it to reach Functions alone", served)
	}

	contained, err := stacks.Provision(context.Background(), provider.StackPlan{
		Ref:  ref,
		Kind: provider.StackApp,
		App: &provider.AppPlan{
			App:             "web",
			Compute:         provider.ComputeContainer,
			Image:           "ocel/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			HealthCheckPath: "/",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() of a container app = %v", err)
	}
	if len(contained.Containers) != 1 || len(contained.Functions) != 0 {
		t.Fatalf("Provision() of a container app = %+v, want it to reach the Containers hooks alone", contained)
	}

	if err := stackrecords.Write(context.Background(), p.Records(), ref.Class, ref.Project, ref.Name, stackrecords.Stack{
		Kind:       provider.StackApp,
		Containers: contained.Containers,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := stacks.Provision(context.Background(), provider.StackPlan{
		Ref:  ref,
		Kind: provider.StackApp,
		App: &provider.AppPlan{
			App:       "web",
			Compute:   provider.ComputeServerless,
			Functions: []provider.FunctionSpec{{Name: "api"}},
		},
	}, nil); err != nil {
		t.Fatalf("Provision() of an app moving back to serverless = %v", err)
	}
	if taken := p.FakeStacks().TakenDown(); !slices.Contains(taken, "web") {
		t.Errorf("the reference provider took down %v, want the container the app left behind: an app changing compute leaves the other primitive's work standing otherwise", taken)
	}
}
