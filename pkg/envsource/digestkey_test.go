package envsource_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
)

func unkeyedDigests(plaintext string) []string {
	sum := sha256.Sum256([]byte(plaintext))
	full := hex.EncodeToString(sum[:])
	return []string{full[:16], full}
}

func everyRecord(t *testing.T, store envvars.Store) []byte {
	t.Helper()
	var all []byte
	for _, root := range []string{records.RootValues, records.RootEnvSources, records.RootEnvSourceStatus, records.RootEnvSourceDigestKey} {
		listed, err := store.Records.List(context.Background(), records.Name{root})
		if err != nil {
			t.Fatal(err)
		}
		for _, recorded := range listed {
			all = append(all, recorded.Bytes...)
		}
	}
	return all
}

func TestASyncedValueCarriesNoDigestOfItsPlaintextThatNeedsNoKey(t *testing.T) {
	t.Parallel()
	sync, store, infisical, host, _ := syncFixture(t)
	infisical.put("/", fakeSecret{id: "s1", key: "DATABASE_URL", value: "postgres://prod", version: 3})
	register(t, store, infisicalRegistration("shop", host, cloudIdentity, ""))
	if err := sync.CopyScheduled(context.Background()); err != nil {
		t.Fatal(err)
	}

	copied := reveal(t, store, scopeOf("shop"), classWide("", "DATABASE_URL"))
	if copied.Plaintext != "postgres://prod" || copied.Provenance.Version == "" {
		t.Fatalf("DATABASE_URL = %+v, want the value copied under a version", copied)
	}
	stored := everyRecord(t, store)
	for _, digest := range unkeyedDigests("postgres://prod") {
		if bytes.Contains(stored, []byte(digest)) {
			t.Fatalf("the records hold %s, a digest of the value anyone can compute from a guess", digest)
		}
	}
}

func TestAValueCopiedUnderAnotherClassKeyGetsAnotherVersion(t *testing.T) {
	t.Parallel()
	versionIn := func() string {
		store, scope := storeFixture()
		key, err := envsource.EnsureDigestKey(context.Background(), store, scope.Class)
		if err != nil {
			t.Fatal(err)
		}
		copyWith(t, store, scope, key, 1, map[envvars.Cell]envsource.Value{cell("", "K"): value("same", "s1@1")})
		return reveal(t, store, scope, classWide("", "K")).Provenance.Version
	}

	if one, other := versionIn(), versionIn(); one == other {
		t.Fatalf("two keys versioned one value as %q alike, want the version keyed", one)
	}
}

func TestTheSameValueUnderTheSameKeyKeepsItsVersionAcrossSyncs(t *testing.T) {
	t.Parallel()
	first, store, infisical, host, _ := syncFixture(t)
	infisical.put("/", fakeSecret{id: "s1", key: "K", value: "v", version: 1})
	registration := infisicalRegistration("shop", host, cloudIdentity, "")
	register(t, store, registration)
	if _, err := first.CopyProject(context.Background(), registration); err != nil {
		t.Fatal(err)
	}
	before := reveal(t, store, scopeOf("shop"), classWide("", "K"))

	restarted := &envsource.Sync{Store: store, Class: first.Class, Login: first.Login, Now: first.Now}
	result, err := restarted.CopyProject(context.Background(), registration)
	if err != nil {
		t.Fatal(err)
	}
	after := reveal(t, store, scopeOf("shop"), classWide("", "K"))
	if len(result.Written) != 0 || after.Version != before.Version || after.Provenance.Version != before.Provenance.Version {
		t.Fatalf("a second sync wrote %v and moved the version from %q to %q, want the unchanged value left alone", result.Written, before.Provenance.Version, after.Provenance.Version)
	}
}

func TestAValueAReferencedSecretsRotationChangedIsCopiedAtTheNextSync(t *testing.T) {
	t.Parallel()
	sync, store, infisical, host, _ := syncFixture(t)
	infisical.put("/", fakeSecret{id: "s1", key: "DATABASE_URL", value: "postgres://one", version: 3})
	registration := infisicalRegistration("shop", host, cloudIdentity, "")
	register(t, store, registration)
	if _, err := sync.CopyProject(context.Background(), registration); err != nil {
		t.Fatal(err)
	}

	infisical.set(func(f *fakeInfisical) {
		f.secrets["/"] = []fakeSecret{{id: "s1", key: "DATABASE_URL", value: "postgres://two", version: 3}}
	})
	if _, err := sync.CopyProject(context.Background(), registration); err != nil {
		t.Fatal(err)
	}
	if copied := reveal(t, store, scopeOf("shop"), classWide("", "DATABASE_URL")); copied.Plaintext != "postgres://two" {
		t.Fatalf("DATABASE_URL = %q, want the value the rotation expanded to", copied.Plaintext)
	}
}

func TestAValueStoredUnderAnUnkeyedVersionIsCopiedAgain(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	ctx := context.Background()
	unkeyed := unkeyedDigests("v")[0]
	for key, stored := range map[string]string{"FROM_INFISICAL": "s1@1#" + unkeyed, "FROM_EXEC": unkeyed} {
		if _, err := store.SetFromEnvSource(ctx, scope, classWide("", key), "v", envvars.Provenance{EnvSource: fromInfisical, Version: stored, ReadAt: readAt(1)}, 0); err != nil {
			t.Fatal(err)
		}
	}

	result := copyAt(t, store, scope, 2, map[envvars.Cell]envsource.Value{
		cell("", "FROM_INFISICAL"): value("v", "s1@1"),
		cell("", "FROM_EXEC"):      value("v", ""),
	})
	if len(result.Written) != 2 {
		t.Fatalf("written = %v, want both values rewritten under a keyed version", result.Written)
	}
	for _, key := range []string{"FROM_INFISICAL", "FROM_EXEC"} {
		if version := reveal(t, store, scope, classWide("", key)).Provenance.Version; bytes.Contains([]byte(version), []byte(unkeyed)) {
			t.Errorf("%s still versioned %q", key, version)
		}
	}
}

func TestADigestKeyThatWillNotOpenStopsTheCopyAndIsNeverReplaced(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	ctx := context.Background()
	if _, err := envsource.EnsureDigestKey(ctx, store, scope.Class); err != nil {
		t.Fatal(err)
	}
	sealedBefore, err := store.Records.Read(ctx, records.Name{records.RootEnvSourceDigestKey, string(scope.Class)})
	if err != nil {
		t.Fatal(err)
	}

	resealed := envvars.Store{Records: store.Records, Cipher: fake.NewCipher()}
	if _, err := envsource.EnsureDigestKey(ctx, resealed, scope.Class); err == nil {
		t.Fatal("EnsureDigestKey() over a key sealed by another cipher succeeded, want the failure to open it")
	}
	sync := &envsource.Sync{Store: resealed, Class: scope.Class}
	registration := envsource.Registration{Project: "shop", Descriptor: execDescriptor(envsource.FormatJSON, "vault"), Folders: []string{""}}
	register(t, resealed, registration)
	if _, err := sync.CopyProjectFrom(ctx, registration, envsource.NewFixed("exec", map[envvars.Cell]envsource.Value{cell("", "K"): value("v", "")})); err == nil {
		t.Fatal("CopyProjectFrom() without a key it can open succeeded, want it refused rather than versioned some weaker way")
	}
	if _, err := store.Get(ctx, scope, classWide("", "K"), false); !errors.Is(err, envvars.ErrNotFound) {
		t.Fatalf("K = %v, want nothing copied", err)
	}
	sealedAfter, err := store.Records.Read(ctx, records.Name{records.RootEnvSourceDigestKey, string(scope.Class)})
	if err != nil || sealedAfter.Revision != sealedBefore.Revision {
		t.Fatalf("the digest key record moved from %s to %s (%v), want it kept", sealedBefore.Revision, sealedAfter.Revision, err)
	}
}

func TestEachClassVersionsAValueUnderItsOwnKey(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	versions := map[edge.Class]string{}
	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		scope := envvars.Scope{Project: "shop", Class: class}
		copyAt(t, store, scope, 1, map[envvars.Cell]envsource.Value{cell("", "K"): value("same", "s1@1")})
		versions[class] = reveal(t, store, scope, classWide("", "K")).Provenance.Version
	}
	if versions[edge.ClassProduction] == versions[edge.ClassPreview] {
		t.Fatalf("production and preview versioned one value as %q alike, want each class keyed apart", versions[edge.ClassPreview])
	}
}
