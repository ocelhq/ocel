package fake_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
)

func TestRecordsWriteIsACompareAndSet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	name := providerkit.RecordName{"edgestacks", "production", "shop"}

	first, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("one")})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if _, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("two")}); !errors.Is(err, providerkit.ErrStale) {
		t.Fatalf("Write() over an existing record without its revision = %v, want ErrStale", err)
	}

	second, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("two"), Revision: first})
	if err != nil {
		t.Fatalf("Write() at the revision it was read at: error = %v", err)
	}
	if second == first {
		t.Error("Write() reused the revision, so a lost update would go unnoticed")
	}

	if _, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("three"), Revision: first}); !errors.Is(err, providerkit.ErrStale) {
		t.Fatalf("Write() at a revision that moved = %v, want ErrStale", err)
	}
}

func TestRecordsWriteRefusesARevisionForARecordThatIsNotThere(t *testing.T) {
	t.Parallel()

	records := fake.NewRecords()
	_, err := records.Write(context.Background(), providerkit.Record{
		Name:     providerkit.RecordName{"schema"},
		Bytes:    []byte("{}"),
		Revision: "1",
	})
	if !errors.Is(err, providerkit.ErrStale) {
		t.Fatalf("Write() = %v, want ErrStale", err)
	}
}

func TestRecordsReadAndRemove(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	name := providerkit.RecordName{"schema"}

	if _, err := records.Read(ctx, name); !errors.Is(err, providerkit.ErrNoRecord) {
		t.Fatalf("Read() of an absent record = %v, want ErrNoRecord", err)
	}

	revision, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("{}")})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	read, err := records.Read(ctx, name)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if string(read.Bytes) != "{}" || read.Revision != revision {
		t.Errorf("Read() = %+v, want the bytes written at revision %q", read, revision)
	}

	if err := records.Remove(ctx, name, "not-the-revision"); !errors.Is(err, providerkit.ErrStale) {
		t.Fatalf("Remove() at the wrong revision = %v, want ErrStale", err)
	}
	if err := records.Remove(ctx, name, revision); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := records.Read(ctx, name); !errors.Is(err, providerkit.ErrNoRecord) {
		t.Fatalf("Read() after Remove = %v, want ErrNoRecord", err)
	}
}

func TestRecordsListIsScopedToThePrefix(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	for _, slug := range []string{"shop", "blog"} {
		if _, err := records.Write(ctx, providerkit.Record{Name: providerkit.RecordName{"projects", slug}}); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if _, err := records.Write(ctx, providerkit.Record{Name: providerkit.RecordName{"schema"}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	found, err := records.List(ctx, providerkit.RecordName{"projects"})
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
	sealer := fake.NewSealer()
	at := providerkit.Coordinate{Project: "shop", Class: providerkit.ClassProduction, Env: "production", Name: "DATABASE_URL"}

	sealed, err := sealer.Seal(ctx, at, []byte("postgres://"))
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if bytes.Contains(sealed, []byte("postgres://")) {
		t.Fatal("Seal() left the plaintext in the sealed bytes")
	}

	opened, err := sealer.Open(ctx, at, sealed)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if string(opened) != "postgres://" {
		t.Errorf("Open() = %q", opened)
	}

	elsewhere := at
	elsewhere.Name = "OTHER_URL"
	if _, err := sealer.Open(ctx, elsewhere, sealed); err == nil {
		t.Fatal("Open() at another coordinate succeeded, want the coordinate to bind the value")
	}
}

func TestArtifactsRemovePrefixLeavesTheRest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	artifacts := fake.NewArtifacts()
	kept := providerkit.ArtifactRef{Class: providerkit.ClassProduction, Bucket: providerkit.StoreFunctions, Key: "other/app.zip"}
	removed := providerkit.ArtifactRef{Class: providerkit.ClassProduction, Bucket: providerkit.StoreAssets, Key: "releases/r1/app.zip"}

	for _, ref := range []providerkit.ArtifactRef{kept, removed} {
		if err := artifacts.Put(ctx, ref, bytes.NewReader([]byte("body"))); err != nil {
			t.Fatalf("Put() error = %v", err)
		}
	}

	if err := artifacts.RemovePrefix(ctx, providerkit.ClassProduction, "releases/", nil); err != nil {
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

	if _, err := fake.New(context.Background(), providerkit.Options{"regoin": "typo"}); err == nil {
		t.Fatal("New() accepted an option it does not know")
	}
	if _, err := fake.New(context.Background(), providerkit.Options{"region": "nowhere"}); err != nil {
		t.Fatalf("New() error = %v", err)
	}
}

func TestOptionalSetsAreSwitchable(t *testing.T) {
	t.Parallel()

	base := fake.NewProvider(fake.Options{})
	for _, tc := range []struct {
		name     string
		provider providerkit.Provider
		want     []string
	}{
		{"none", base, nil},
		{"Warmer", fake.Warmer{Provider: base}, []string{"Warmer"}},
		{"CodeEmbedder", fake.CodeEmbedder{Provider: base}, []string{"CodeEmbedder"}},
		{"StackInspector", fake.StackInspector{Provider: base}, []string{"StackInspector"}},
		{"GrantVerifier", fake.GrantVerifier{Provider: base}, []string{"GrantVerifier"}},
		{"Full", fake.Full{Provider: base}, []string{"Warmer", "CodeEmbedder", "StackInspector", "GrantVerifier"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := setsOn(tc.provider); !slices.Equal(got, tc.want) {
				t.Errorf("optional sets on %s = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func setsOn(p providerkit.Provider) []string {
	var found []string
	if _, ok := p.(providerkit.Warmer); ok {
		found = append(found, "Warmer")
	}
	if _, ok := p.(providerkit.CodeEmbedder); ok {
		found = append(found, "CodeEmbedder")
	}
	if _, ok := p.(providerkit.StackInspector); ok {
		found = append(found, "StackInspector")
	}
	if _, ok := p.(providerkit.GrantVerifier); ok {
		found = append(found, "GrantVerifier")
	}
	return found
}

func TestTheReferenceProviderIsReachedThroughThePrimitiveItsAppsComputeNames(t *testing.T) {
	t.Parallel()

	provider := fake.Full{Provider: fake.NewProvider(fake.Options{})}
	releaser := resources.Releaser(provider.Records(), provider.Artifacts(), provider)
	ref := providerkit.StackRef{
		Project: "shop",
		Class:   providerkit.ClassProduction,
		Name:    naming.AppStack("prod", "web", naming.NewRelease("d1", "f1")),
	}

	served, err := releaser.Provision(context.Background(), providerkit.StackPlan{
		Ref:  ref,
		Kind: providerkit.StackApp,
		App: &providerkit.AppPlan{
			App:       "web",
			Compute:   providerkit.ComputeServerless,
			Functions: []providerkit.FunctionSpec{{Name: "api"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() of a serverless app = %v", err)
	}
	if len(served.Functions) != 1 || len(served.Containers) != 0 {
		t.Fatalf("Provision() of a serverless app = %+v, want it to reach Functions alone", served)
	}

	contained, err := releaser.Provision(context.Background(), providerkit.StackPlan{
		Ref:  ref,
		Kind: providerkit.StackApp,
		App: &providerkit.AppPlan{
			App:             "web",
			Compute:         providerkit.ComputeContainer,
			Image:           "ocel/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			HealthCheckPath: "/",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() of a container app = %v", err)
	}
	if len(contained.Containers) != 1 || len(contained.Functions) != 0 {
		t.Fatalf("Provision() of a container app = %+v, want it to reach AppContainers alone", contained)
	}
}
