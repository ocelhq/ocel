package envsource_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

type stubSource struct {
	id       string
	held     map[values.Cell]envsource.Resolved
	err      error
	resolved [][]string
	put      []values.Cell
}

func (s *stubSource) ID() string { return s.id }

func (s *stubSource) Capabilities() envsource.Caps {
	return envsource.Caps{Read: true, List: true, Write: true, Standing: true}
}

func (s *stubSource) Resolve(_ context.Context, folders []string) (map[values.Cell]envsource.Resolved, error) {
	s.resolved = append(s.resolved, slices.Clone(folders))
	if s.err != nil {
		return nil, s.err
	}
	out := map[values.Cell]envsource.Resolved{}
	for at, held := range s.held {
		if slices.Contains(folders, at.Folder) {
			out[at] = held
		}
	}
	return out, nil
}

func (s *stubSource) Put(_ context.Context, at values.Cell, value []byte, _ string) error {
	if _, held := s.held[at]; held {
		return envsource.ErrExists
	}
	s.put = append(s.put, at)
	s.held[at] = envsource.Resolved{Value: value, Version: "new@1"}
	return nil
}

func (s *stubSource) Link(at values.Cell) string { return "https://source.example/" + at.Key }

func storeFixture() (values.Store, values.Scope) {
	return values.Store{Records: fake.NewRecords(), Sealer: fake.NewSealer()},
		values.Scope{Project: "shop", Class: ports.ClassProduction}
}

func classWide(folder, key string) values.Coordinate {
	return values.Coordinate{Cell: values.Cell{Folder: folder, Key: key}}
}

func reveal(t *testing.T, store values.Store, scope values.Scope, at values.Coordinate) values.Value {
	t.Helper()
	held, err := store.Get(context.Background(), scope, at, true)
	if err != nil {
		t.Fatalf("Get(%s) = %v", at, err)
	}
	return held
}

func TestMirrorCopiesTheSourceIntoTheClassWideCellsAndWritesOnlyWhatChanged(t *testing.T) {
	store, scope := storeFixture()
	ctx := context.Background()
	source := &stubSource{id: "infisical:p/prod", held: map[values.Cell]envsource.Resolved{
		{Key: "DATABASE_URL"}:              {Value: []byte("postgres://one"), Version: "s1@1"},
		{Folder: "/web", Key: "API_KEY"}:   {Value: []byte("key"), Version: "s2@4"},
		{Folder: "/other", Key: "UNREAD"}:  {Value: []byte("never"), Version: "s3@1"},
		{Folder: "/web", Key: "SIGNED_BY"}: {Value: []byte("k"), Version: "s4@1"},
	}}

	report, err := envsource.Mirror(ctx, store, scope, source, []string{"", "/web"}, nil)
	if err != nil {
		t.Fatalf("Mirror() = %v", err)
	}
	if len(report.Written) != 3 || len(report.Present) != 3 {
		t.Fatalf("report = %+v, want three cells written from the two folders read", report)
	}
	database := reveal(t, store, scope, classWide("", "DATABASE_URL"))
	if database.Plaintext != "postgres://one" || database.Provenance != (values.Provenance{EnvSource: "infisical:p/prod", Version: "s1@1"}) {
		t.Fatalf("DATABASE_URL = %+v", database)
	}
	if _, err := store.Get(ctx, scope, classWide("/other", "UNREAD"), false); !errors.Is(err, values.ErrNotFound) {
		t.Fatalf("a folder nothing reads was mirrored: %v", err)
	}

	source.held[values.Cell{Key: "DATABASE_URL"}] = envsource.Resolved{Value: []byte("postgres://two"), Version: "s1@2"}
	again, err := envsource.Mirror(ctx, store, scope, source, []string{"", "/web"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(again.Written, []values.Cell{{Key: "DATABASE_URL"}}) || again.Unchanged != 2 {
		t.Fatalf("second Mirror() = %+v, want only the changed cell written", again)
	}
	if held := reveal(t, store, scope, classWide("", "DATABASE_URL")); held.Plaintext != "postgres://two" || held.Version != 2 {
		t.Fatalf("DATABASE_URL = %+v, want the new value at version 2", held)
	}
	if held := reveal(t, store, scope, classWide("/web", "API_KEY")); held.Version != 1 {
		t.Fatalf("API_KEY = %+v, want it left at version 1", held)
	}
}

func TestMirrorNeverWritesAnOlderReadOverANewerOne(t *testing.T) {
	store, scope := storeFixture()
	ctx := context.Background()
	newer := &stubSource{id: "infisical:p/prod", held: map[values.Cell]envsource.Resolved{{Key: "K"}: {Value: []byte("new"), Version: "s1@5"}}}
	older := &stubSource{id: "infisical:p/prod", held: map[values.Cell]envsource.Resolved{{Key: "K"}: {Value: []byte("old"), Version: "s1@4"}}}

	if _, err := envsource.Mirror(ctx, store, scope, newer, []string{""}, nil); err != nil {
		t.Fatal(err)
	}
	report, err := envsource.Mirror(ctx, store, scope, older, []string{""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Written) != 0 {
		t.Fatalf("an older read wrote %v", report.Written)
	}
	if held := reveal(t, store, scope, classWide("", "K")); held.Plaintext != "new" {
		t.Fatalf("K = %q, want the newer read kept", held.Plaintext)
	}

	recreated := &stubSource{id: "infisical:p/prod", held: map[values.Cell]envsource.Resolved{{Key: "K"}: {Value: []byte("recreated"), Version: "s9@1"}}}
	if _, err := envsource.Mirror(ctx, store, scope, recreated, []string{""}, nil); err != nil {
		t.Fatal(err)
	}
	if held := reveal(t, store, scope, classWide("", "K")); held.Plaintext != "recreated" {
		t.Fatalf("K = %q, want a key deleted and recreated in the source taken", held.Plaintext)
	}
}

func TestMirrorRemovesWhatTheSourceDroppedAndSparesWhatOcelOwns(t *testing.T) {
	store, scope := storeFixture()
	ctx := context.Background()
	source := &stubSource{id: "infisical:p/prod", held: map[values.Cell]envsource.Resolved{
		{Key: "GONE"}: {Value: []byte("x"), Version: "s1@1"},
		{Key: "KEPT"}: {Value: []byte("y"), Version: "s2@1"},
	}}
	if _, err := envsource.Mirror(ctx, store, scope, source, []string{""}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, scope, classWide("", "SET_BEFORE_THE_SOURCE"), "mine", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, scope, values.Coordinate{Cell: values.Cell{Key: "GONE"}, Environment: "pr-1"}, "override", nil); err != nil {
		t.Fatal(err)
	}

	delete(source.held, values.Cell{Key: "GONE"})
	report, err := envsource.Mirror(ctx, store, scope, source, []string{""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(report.Removed, []values.Cell{{Key: "GONE"}}) {
		t.Fatalf("removed = %v, want the dropped key", report.Removed)
	}
	if _, err := store.Get(ctx, scope, classWide("", "GONE"), false); !errors.Is(err, values.ErrNotFound) {
		t.Fatalf("GONE = %v, want it removed", err)
	}
	if held := reveal(t, store, scope, classWide("", "SET_BEFORE_THE_SOURCE")); held.Plaintext != "mine" {
		t.Fatalf("a value ocel held before the source was removed")
	}
	if held := reveal(t, store, scope, values.Coordinate{Cell: values.Cell{Key: "GONE"}, Environment: "pr-1"}); held.Plaintext != "override" {
		t.Fatal("a named environment's override was removed")
	}
}

func TestMirrorLeavesTheCredentialCellsAlone(t *testing.T) {
	store, scope := storeFixture()
	ctx := context.Background()
	if _, err := store.Set(ctx, scope, classWide("", "INFISICAL_CLIENT_SECRET"), "real", nil); err != nil {
		t.Fatal(err)
	}
	source := &stubSource{id: "infisical:p/prod", held: map[values.Cell]envsource.Resolved{
		{Key: "INFISICAL_CLIENT_SECRET"}: {Value: []byte("from-the-source"), Version: "s1@1"},
	}}
	report, err := envsource.Mirror(ctx, store, scope, source, []string{""}, []values.Cell{{Key: "INFISICAL_CLIENT_SECRET"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Written) != 0 {
		t.Fatalf("written = %v, want a credential cell left alone", report.Written)
	}
	if held := reveal(t, store, scope, classWide("", "INFISICAL_CLIENT_SECRET")); held.Plaintext != "real" {
		t.Fatalf("credential = %q", held.Plaintext)
	}
}

func TestMirrorKeepsWhatItLastWroteWhenTheSourceFails(t *testing.T) {
	store, scope := storeFixture()
	ctx := context.Background()
	source := &stubSource{id: "infisical:p/prod", held: map[values.Cell]envsource.Resolved{{Key: "K"}: {Value: []byte("v"), Version: "s1@1"}}}
	if _, err := envsource.Mirror(ctx, store, scope, source, []string{""}, nil); err != nil {
		t.Fatal(err)
	}
	source.err = errors.New("Infisical answered 503")
	if _, err := envsource.Mirror(ctx, store, scope, source, []string{""}, nil); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("Mirror() over a failing source = %v, want its error", err)
	}
	if held := reveal(t, store, scope, classWide("", "K")); held.Plaintext != "v" {
		t.Fatal("a failed read dropped the last synced value")
	}
}
