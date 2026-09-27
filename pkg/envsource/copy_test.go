package envsource_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const fromInfisical = "infisical:p/prod"

func storeFixture() (envvars.Store, envvars.Scope) {
	return envvars.Store{Records: fake.NewRecords(), Cipher: fake.NewCipher()},
		envvars.Scope{Project: "shop", Class: edge.ClassProduction}
}

func classWide(folder, key string) envvars.Coordinate {
	return envvars.Coordinate{Cell: envvars.Cell{Folder: folder, Key: key}}
}

func reveal(t *testing.T, store envvars.Store, scope envvars.Scope, at envvars.Coordinate) envvars.Value {
	t.Helper()
	value, err := store.Get(context.Background(), scope, at, true)
	if err != nil {
		t.Fatalf("Get(%s) = %v", at, err)
	}
	return value
}

func readAt(second int) time.Time { return time.Unix(1_800_000_000+int64(second), 0) }

func value(plaintext, version string) envsource.Value {
	return envsource.Value{Plaintext: []byte(plaintext), Version: version}
}

func TestCopyValuesWritesTheReadFoldersIntoClassWideValuesAndOnlyWhatChanged(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	ctx := context.Background()
	read := map[envvars.Cell]envsource.Value{
		cell("", "DATABASE_URL"):  value("postgres://one", "s1@1"),
		cell("/web", "API_KEY"):   value("key", "s2@4"),
		cell("/other", "UNREAD"):  value("never", "s3@1"),
		cell("/web", "SIGNED_BY"): value("k", "s4@1"),
	}

	result, err := envsource.CopyValues(ctx, store, scope, digestKeyOf(t, store, scope), fromInfisical, readAt(10), read, []string{"", "/web"}, nil)
	if err != nil {
		t.Fatalf("CopyValues() = %v", err)
	}
	if len(result.Written) != 3 || len(result.Present) != 3 || result.EnvSource != fromInfisical {
		t.Fatalf("result = %+v, want three values written from the two folders read", result)
	}
	database := reveal(t, store, scope, classWide("", "DATABASE_URL"))
	if database.Plaintext != "postgres://one" || database.Provenance.EnvSource != fromInfisical || !strings.HasPrefix(database.Provenance.Version, "s1@1#") {
		t.Fatalf("DATABASE_URL = %+v", database)
	}
	if _, err := store.Get(ctx, scope, classWide("/other", "UNREAD"), false); !errors.Is(err, envvars.ErrNotFound) {
		t.Fatalf("a folder nothing reads was copied: %v", err)
	}

	read[cell("", "DATABASE_URL")] = value("postgres://two", "s1@2")
	again, err := envsource.CopyValues(ctx, store, scope, digestKeyOf(t, store, scope), fromInfisical, readAt(20), read, []string{"", "/web"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(again.Written, []envvars.Cell{cell("", "DATABASE_URL")}) || len(again.Unchanged) != 2 {
		t.Fatalf("second CopyValues() = %+v, want only the changed value written", again)
	}
	if database := reveal(t, store, scope, classWide("", "DATABASE_URL")); database.Plaintext != "postgres://two" || database.Version != 2 {
		t.Fatalf("DATABASE_URL = %+v, want the new value at version 2", database)
	}
	if key := reveal(t, store, scope, classWide("/web", "API_KEY")); key.Version != 1 {
		t.Fatalf("API_KEY = %+v, want it left at version 1", key)
	}
}

func TestCopyValuesNeverWritesAnOlderReadOverANewerOne(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	ctx := context.Background()
	copyOne := func(plaintext, version string) envsource.CopyResult {
		t.Helper()
		result, err := envsource.CopyValues(ctx, store, scope, digestKeyOf(t, store, scope), fromInfisical, readAt(30), map[envvars.Cell]envsource.Value{cell("", "K"): value(plaintext, version)}, []string{""}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	copyOne("new", "s1@5")
	if older := copyOne("old", "s1@4"); len(older.Written) != 0 {
		t.Fatalf("an older read wrote %v", older.Written)
	}
	if k := reveal(t, store, scope, classWide("", "K")); k.Plaintext != "new" {
		t.Fatalf("K = %q, want the newer read kept", k.Plaintext)
	}
	copyOne("recreated", "s9@1")
	if k := reveal(t, store, scope, classWide("", "K")); k.Plaintext != "recreated" {
		t.Fatalf("K = %q, want a key deleted and recreated in the source taken", k.Plaintext)
	}
}

func TestCopyValuesWritesAValueThatChangedUnderTheVersionItWasReadAt(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	copyAt(t, store, scope, 1, map[envvars.Cell]envsource.Value{cell("", "K"): value("before", "s1@3")})

	if changed := copyAt(t, store, scope, 2, map[envvars.Cell]envsource.Value{cell("", "K"): value("after", "s1@3")}); len(changed.Written) != 1 {
		t.Fatalf("a value that changed under an unchanged secret version wrote %v, want it written", changed.Written)
	}
	if k := reveal(t, store, scope, classWide("", "K")); k.Plaintext != "after" {
		t.Fatalf("K = %q, want the value a referenced secret's rotation changed", k.Plaintext)
	}
	if older := copyAt(t, store, scope, 3, map[envvars.Cell]envsource.Value{cell("", "K"): value("older", "s1@2")}); len(older.Written) != 0 {
		t.Fatalf("a read of an older secret version wrote %v", older.Written)
	}
}

func copyAt(t *testing.T, store envvars.Store, scope envvars.Scope, second int, read map[envvars.Cell]envsource.Value) envsource.CopyResult {
	t.Helper()
	return copyWith(t, store, scope, digestKeyOf(t, store, scope), second, read)
}

func copyWith(t *testing.T, store envvars.Store, scope envvars.Scope, key envsource.DigestKey, second int, read map[envvars.Cell]envsource.Value) envsource.CopyResult {
	t.Helper()
	result, err := envsource.CopyValues(context.Background(), store, scope, key, fromInfisical, readAt(second), read, []string{""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func digestKeyOf(t *testing.T, store envvars.Store, scope envvars.Scope) envsource.DigestKey {
	t.Helper()
	key, err := envsource.EnsureDigestKey(context.Background(), store, scope.Class)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestAReadOlderThanTheOneThatRemovedAKeyNeverBringsItBack(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	copyAt(t, store, scope, 1, map[envvars.Cell]envsource.Value{cell("", "K"): value("v", "s1@1")})
	copyAt(t, store, scope, 30, map[envvars.Cell]envsource.Value{})

	copyAt(t, store, scope, 20, map[envvars.Cell]envsource.Value{cell("", "K"): value("v", "s1@1")})
	if _, err := store.Get(context.Background(), scope, classWide("", "K"), false); !errors.Is(err, envvars.ErrNotFound) {
		t.Fatalf("K = %v, want it kept removed: the read that saw it was older than the one that removed it", err)
	}
}

func TestAReadOlderThanTheOneThatWroteAKeyNeverRemovesIt(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	copyAt(t, store, scope, 30, map[envvars.Cell]envsource.Value{cell("", "K"): value("v", "s1@1")})

	copyAt(t, store, scope, 20, map[envvars.Cell]envsource.Value{})
	if k := reveal(t, store, scope, classWide("", "K")); k.Plaintext != "v" {
		t.Fatalf("K = %q, want it kept: the read that lacked it was older than the one that wrote it", k.Plaintext)
	}
}

func TestAReadOlderThanTheOneThatWroteAKeyRecreatedInTheSourceNeverOverwritesIt(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	copyAt(t, store, scope, 30, map[envvars.Cell]envsource.Value{cell("", "K"): value("recreated", "s9@1")})

	copyAt(t, store, scope, 20, map[envvars.Cell]envsource.Value{cell("", "K"): value("old", "s1@5")})
	if k := reveal(t, store, scope, classWide("", "K")); k.Plaintext != "recreated" {
		t.Fatalf("K = %q, want the newer read's value kept whatever its secret id", k.Plaintext)
	}
}

func TestCopyValuesRemovesWhatTheSourceDroppedAndSparesNamedEnvironments(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	ctx := context.Background()
	read := map[envvars.Cell]envsource.Value{cell("", "GONE"): value("x", "s1@1"), cell("", "KEPT"): value("y", "s2@1")}
	if _, err := envsource.CopyValues(ctx, store, scope, digestKeyOf(t, store, scope), fromInfisical, readAt(40), read, []string{""}, nil); err != nil {
		t.Fatal(err)
	}
	override := envvars.Coordinate{Cell: envvars.Cell{Key: "GONE"}, Environment: "pr-1"}
	if _, err := store.Set(ctx, scope, override, "override", nil); err != nil {
		t.Fatal(err)
	}

	delete(read, cell("", "GONE"))
	result, err := envsource.CopyValues(ctx, store, scope, digestKeyOf(t, store, scope), fromInfisical, readAt(50), read, []string{""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Removed, []envvars.Cell{cell("", "GONE")}) {
		t.Fatalf("removed = %v, want the dropped key", result.Removed)
	}
	if _, err := store.Get(ctx, scope, classWide("", "GONE"), false); !errors.Is(err, envvars.ErrNotFound) {
		t.Fatalf("GONE = %v, want it removed", err)
	}
	if kept := reveal(t, store, scope, override); kept.Plaintext != "override" {
		t.Fatal("a named environment's override was removed")
	}
}

func TestATierSwitchedToAnEnvSourceKeepsNoClassWideValueTheSourceLacks(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	ctx := context.Background()
	before := map[envvars.Cell]envsource.Value{cell("", "FROM_STAGING"): value("s", "s7@1")}
	if _, err := envsource.CopyValues(ctx, store, scope, digestKeyOf(t, store, scope), "infisical:p/staging", readAt(60), before, []string{""}, nil); err != nil {
		t.Fatal(err)
	}
	for key, plaintext := range map[string]string{"SET_IN_OCEL": "mine", "ALSO_IN_THE_SOURCE": "old", "INFISICAL_CLIENT_SECRET": "credential"} {
		if _, err := store.Set(ctx, scope, classWide("", key), plaintext, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Set(ctx, scope, classWide("/other", "UNREAD_FOLDER"), "kept", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, envvars.Scope{Project: "shared", Class: scope.Class}, classWide("", "TARGET"), "t", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReference(ctx, scope, classWide("", "REFERENCED"), envvars.Target{Project: "shared", Cell: envvars.Cell{Key: "TARGET"}}); err != nil {
		t.Fatal(err)
	}

	read := map[envvars.Cell]envsource.Value{cell("", "ALSO_IN_THE_SOURCE"): value("new", "s1@1")}
	keep := []envvars.Cell{cell("", "INFISICAL_CLIENT_SECRET")}
	result, err := envsource.CopyValues(ctx, store, scope, digestKeyOf(t, store, scope), fromInfisical, readAt(70), read, []string{""}, keep)
	if err != nil {
		t.Fatal(err)
	}
	if want := []envvars.Cell{cell("", "FROM_STAGING"), cell("", "SET_IN_OCEL")}; !slices.Equal(result.Removed, want) {
		t.Fatalf("removed = %v, want %v: the source is the one writer of every class-wide value it reads", result.Removed, want)
	}
	for _, key := range []string{"SET_IN_OCEL", "FROM_STAGING"} {
		if _, err := store.Get(ctx, scope, classWide("", key), false); !errors.Is(err, envvars.ErrNotFound) {
			t.Errorf("%s = %v, want it removed so the gate reads it as missing", key, err)
		}
	}
	if taken := reveal(t, store, scope, classWide("", "ALSO_IN_THE_SOURCE")); taken.Plaintext != "new" || taken.Provenance.EnvSource != fromInfisical {
		t.Errorf("ALSO_IN_THE_SOURCE = %+v, want the source's value", taken)
	}
	for _, at := range []envvars.Coordinate{classWide("", "INFISICAL_CLIENT_SECRET"), classWide("", "REFERENCED"), classWide("/other", "UNREAD_FOLDER")} {
		if _, err := store.Get(ctx, scope, at, false); err != nil {
			t.Errorf("%s = %v, want a credential, a reference and a folder the source does not read left alone", at, err)
		}
	}
}

func TestCopyValuesLeavesTheCellsItIsToldToKeep(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	ctx := context.Background()
	if _, err := store.Set(ctx, scope, classWide("", "INFISICAL_CLIENT_SECRET"), "real", nil); err != nil {
		t.Fatal(err)
	}
	read := map[envvars.Cell]envsource.Value{cell("", "INFISICAL_CLIENT_SECRET"): value("from-the-source", "s1@1")}
	result, err := envsource.CopyValues(ctx, store, scope, digestKeyOf(t, store, scope), fromInfisical, readAt(80), read, []string{""}, []envvars.Cell{cell("", "INFISICAL_CLIENT_SECRET")})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Written) != 0 {
		t.Fatalf("written = %v, want a kept cell left alone", result.Written)
	}
	if credential := reveal(t, store, scope, classWide("", "INFISICAL_CLIENT_SECRET")); credential.Plaintext != "real" {
		t.Fatalf("credential = %q", credential.Plaintext)
	}
}

func TestCopyValuesRefusesAValueTooLargeToStoreAndWritesTheRest(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	read := map[envvars.Cell]envsource.Value{
		cell("", "HUGE"): value(strings.Repeat("x", envvars.MaxValueBytes+1), "s1@1"),
		cell("", "FINE"): value("ok", "s2@1"),
	}
	result, err := envsource.CopyValues(context.Background(), store, scope, digestKeyOf(t, store, scope), fromInfisical, readAt(90), read, []string{""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, refused := result.Refused[cell("", "HUGE")]; !refused || !slices.Equal(result.Written, []envvars.Cell{cell("", "FINE")}) {
		t.Fatalf("result = %+v, want HUGE refused and FINE written", result)
	}
}

func TestAFixedEnvSourceReadsOnlyTheFoldersAskedFor(t *testing.T) {
	t.Parallel()
	source := envsource.NewFixed("exec", map[envvars.Cell]envsource.Value{cell("", "ROOT"): value("r", "v1"), cell("/web", "WEB"): value("w", "v2")})
	read, err := source.Read(context.Background(), []string{"/web"})
	if err != nil || len(read) != 1 || string(read[cell("/web", "WEB")].Plaintext) != "w" {
		t.Fatalf("Read() = %v, %v, want /web alone", read, err)
	}
	if source.ID() != "exec" {
		t.Errorf("ID() = %q", source.ID())
	}
	if err := source.Create(context.Background(), cell("", "NEW"), []byte("v"), ""); !errors.Is(err, envsource.ErrReadOnly) {
		t.Errorf("Create() = %v, want ErrReadOnly", err)
	}
}
