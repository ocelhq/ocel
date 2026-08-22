package conformance

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func runPorts(t *testing.T, suite Suite) {
	t.Helper()

	if suite.New == nil {
		t.Skip("the suite carries no constructor, so there are no ports to exercise")
	}
	provider, err := suite.New(context.Background(), suite.Options)
	if err != nil {
		t.Fatalf("New() error = %v, want a provider", err)
	}

	t.Run("RecordStore", func(t *testing.T) { runRecordStore(t, provider.Records()) })
	t.Run("Sealer", func(t *testing.T) { runSealer(t, provider.Sealer()) })
}

func under(t *testing.T, rest ...string) providerkit.RecordName {
	return append(providerkit.RecordName{"conformance", t.Name()}, rest...)
}

func runRecordStore(t *testing.T, records providerkit.RecordStore) {
	ctx := context.Background()

	t.Run("an unwritten name is no record", func(t *testing.T) {
		if _, err := records.Read(ctx, under(t, "never-written")); !errors.Is(err, providerkit.ErrNoRecord) {
			t.Fatalf("Read() of a name never written = %v, want ErrNoRecord", err)
		}
	})

	t.Run("a first write claims the name", func(t *testing.T) {
		name := under(t, "claimed")
		revision, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("one")})
		if err != nil {
			t.Fatalf("Write() of a new record = %v, want it stored", err)
		}
		if revision == "" {
			t.Fatal("Write() returned an empty revision, and a compare-and-set has nothing to compare")
		}
		held, err := records.Read(ctx, name)
		if err != nil || !bytes.Equal(held.Bytes, []byte("one")) {
			t.Fatalf("Read() = %q, %v, want the bytes just written", held.Bytes, err)
		}
		if held.Revision != revision {
			t.Fatalf("Read() revision = %q, want the %q Write() reported", held.Revision, revision)
		}
	})

	t.Run("a second write at the same name must name a revision", func(t *testing.T) {
		name := under(t, "occupied")
		if _, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("one")}); err != nil {
			t.Fatal(err)
		}
		if _, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("two")}); !errors.Is(err, providerkit.ErrStale) {
			t.Fatalf("Write() at a taken name with no revision = %v, want ErrStale", err)
		}
	})

	t.Run("a write at the revision read wins and a later one loses", func(t *testing.T) {
		name := under(t, "compared")
		if _, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("one")}); err != nil {
			t.Fatal(err)
		}
		held, err := records.Read(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		held.Bytes = []byte("two")
		if _, err := records.Write(ctx, held); err != nil {
			t.Fatalf("Write() at the revision read = %v, want it stored", err)
		}
		held.Bytes = []byte("three")
		if _, err := records.Write(ctx, held); !errors.Is(err, providerkit.ErrStale) {
			t.Fatalf("a second write at a revision that moved = %v, want ErrStale", err)
		}
	})

	t.Run("a removal names the revision it read", func(t *testing.T) {
		name := under(t, "removed")
		revision, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("one")})
		if err != nil {
			t.Fatal(err)
		}
		if err := records.Remove(ctx, name, "a revision nobody wrote"); !errors.Is(err, providerkit.ErrStale) {
			t.Fatalf("Remove() at a revision that never held = %v, want ErrStale", err)
		}
		if err := records.Remove(ctx, name, revision); err != nil {
			t.Fatalf("Remove() at the revision held = %v, want it gone", err)
		}
		if _, err := records.Read(ctx, name); !errors.Is(err, providerkit.ErrNoRecord) {
			t.Fatalf("Read() after Remove() = %v, want ErrNoRecord", err)
		}
	})

	t.Run("List answers with everything under a prefix", func(t *testing.T) {
		leaves := []providerkit.RecordName{
			under(t, "tree", "a"),
			under(t, "tree", "b", "one"),
			under(t, "tree", "b", "two"),
		}
		for _, name := range leaves {
			if _, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte(name.String())}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := records.Write(ctx, providerkit.Record{Name: under(t, "treeish"), Bytes: []byte("not under it")}); err != nil {
			t.Fatal(err)
		}

		held, err := records.List(ctx, under(t, "tree"))
		if err != nil {
			t.Fatalf("List() = %v", err)
		}
		if len(held) != len(leaves) {
			t.Fatalf("List() returned %d records, want the %d written under the prefix and nothing beside them", len(held), len(leaves))
		}
		for _, record := range held {
			if !bytes.Equal(record.Bytes, []byte(record.Name.String())) {
				t.Errorf("List() returned %s carrying %q, want the bytes written at that name", record.Name, record.Bytes)
			}
			if record.Revision == "" {
				t.Errorf("List() returned %s with no revision, and a caller cannot then remove it", record.Name)
			}
		}

		deeper, err := records.List(ctx, under(t, "tree", "b"))
		if err != nil || len(deeper) != 2 {
			t.Fatalf("List() of a deeper prefix returned %d records, %v, want 2", len(deeper), err)
		}
	})
}

func runSealer(t *testing.T, sealer providerkit.Sealer) {
	ctx := context.Background()

	at := providerkit.Coordinate{
		Project: "shop",
		Class:   providerkit.ClassProduction,
		Env:     "*",
		Folder:  "/",
		Name:    "DATABASE_URL",
	}
	plaintext := []byte("postgres://example")

	sealed, err := sealer.Seal(ctx, at, plaintext)
	if err != nil {
		t.Fatalf("Seal() = %v", err)
	}
	if bytes.Contains(sealed, plaintext) {
		t.Fatal("Seal() returned the plaintext inside its output")
	}

	opened, err := sealer.Open(ctx, at, sealed)
	if err != nil {
		t.Fatalf("Open() at the coordinate sealed = %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("Open() = %q, want %q", opened, plaintext)
	}

	for name, moved := range map[string]providerkit.Coordinate{
		"another project":     {Project: "other", Class: at.Class, Env: at.Env, Folder: at.Folder, Name: at.Name},
		"another class":       {Project: at.Project, Class: providerkit.ClassPreview, Env: at.Env, Folder: at.Folder, Name: at.Name},
		"another environment": {Project: at.Project, Class: at.Class, Env: "staging", Folder: at.Folder, Name: at.Name},
		"another folder":      {Project: at.Project, Class: at.Class, Env: at.Env, Folder: "/apps/web", Name: at.Name},
		"another key":         {Project: at.Project, Class: at.Class, Env: at.Env, Folder: at.Folder, Name: "API_KEY"},
	} {
		t.Run("a value sealed here does not open at "+name, func(t *testing.T) {
			if _, err := sealer.Open(ctx, moved, sealed); err == nil {
				t.Fatal("Open() at a coordinate the value was not sealed at succeeded, so the coordinate is not authenticated")
			}
		})
	}
}
