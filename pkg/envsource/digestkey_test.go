package envsource_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func digestKeyAt(tier environment.Tier) keyvalue.Key {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootEnvSourceDigestKey}.Key("digestkey")
}

func unkeyedDigests(plaintext string) []string {
	sum := sha256.Sum256([]byte(plaintext))
	full := hex.EncodeToString(sum[:])
	return []string{full[:16], full}
}

func everyRecord(t *testing.T, store variablestore.Store) []byte {
	t.Helper()
	var all []byte
	var partitions []keyvalue.Partition
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		partitions = append(partitions, variablestore.ValuesPartition(variablestore.Scope{Project: "shop", Tier: tier}))
		for _, root := range []keyvalue.Root{keyvalue.RootEnvSources, keyvalue.RootEnvSourceStatus, keyvalue.RootEnvSourceDigestKey} {
			partitions = append(partitions, keyvalue.Partition{Tier: tier, Root: root})
		}
	}
	for _, in := range partitions {
		listed, err := store.KeyValues.List(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		for _, recorded := range listed {
			all = append(all, recorded.Value...)
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

	copied := reveal(t, store, scopeOf("shop"), tierWide("", "DATABASE_URL"))
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

func TestAValueCopiedUnderAnotherTierKeyGetsAnotherVersion(t *testing.T) {
	t.Parallel()
	versionIn := func() string {
		store, scope := storeFixture()
		key, err := envsource.EnsureDigestKey(context.Background(), store, scope.Tier)
		if err != nil {
			t.Fatal(err)
		}
		copyWith(t, store, scope, key, 1, map[variablestore.Cell]envsource.Value{cell("", "K"): value("same", "s1@1")})
		return reveal(t, store, scope, tierWide("", "K")).Provenance.Version
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
	before := reveal(t, store, scopeOf("shop"), tierWide("", "K"))

	restarted := &envsource.Sync{Store: store, Tier: first.Tier, Login: first.Login, Now: first.Now}
	result, err := restarted.CopyProject(context.Background(), registration)
	if err != nil {
		t.Fatal(err)
	}
	after := reveal(t, store, scopeOf("shop"), tierWide("", "K"))
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
	if copied := reveal(t, store, scopeOf("shop"), tierWide("", "DATABASE_URL")); copied.Plaintext != "postgres://two" {
		t.Fatalf("DATABASE_URL = %q, want the value the rotation expanded to", copied.Plaintext)
	}
}

func TestAValueStoredUnderAnUnkeyedVersionIsCopiedAgain(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	ctx := context.Background()
	unkeyed := unkeyedDigests("v")[0]
	for key, stored := range map[string]string{"FROM_INFISICAL": "s1@1#" + unkeyed, "FROM_EXEC": unkeyed} {
		if _, err := store.SetFromEnvSource(ctx, scope, tierWide("", key), "v", variablestore.Provenance{EnvSource: fromInfisical, Version: stored, ReadAt: readAt(1)}, 0); err != nil {
			t.Fatal(err)
		}
	}

	result := copyAt(t, store, scope, 2, map[variablestore.Cell]envsource.Value{
		cell("", "FROM_INFISICAL"): value("v", "s1@1"),
		cell("", "FROM_EXEC"):      value("v", ""),
	})
	if len(result.Written) != 2 {
		t.Fatalf("written = %v, want both values rewritten under a keyed version", result.Written)
	}
	for _, key := range []string{"FROM_INFISICAL", "FROM_EXEC"} {
		if version := reveal(t, store, scope, tierWide("", key)).Provenance.Version; bytes.Contains([]byte(version), []byte(unkeyed)) {
			t.Errorf("%s still versioned %q", key, version)
		}
	}
}

func TestADigestKeyThatWillNotOpenStopsTheCopyAndIsNeverReplaced(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	ctx := context.Background()
	if _, err := envsource.EnsureDigestKey(ctx, store, scope.Tier); err != nil {
		t.Fatal(err)
	}
	sealedBefore, err := store.KeyValues.Read(ctx, digestKeyAt(scope.Tier))
	if err != nil {
		t.Fatal(err)
	}

	resealed := variablestore.Store{KeyValues: store.KeyValues, Cipher: fake.NewCipher()}
	if _, err := envsource.EnsureDigestKey(ctx, resealed, scope.Tier); err == nil {
		t.Fatal("EnsureDigestKey() over a key sealed by another cipher succeeded, want the failure to open it")
	}
	sync := &envsource.Sync{Store: resealed, Tier: scope.Tier}
	registration := envsource.Registration{Project: "shop", Descriptor: execDescriptor(envsource.FormatJSON, "vault"), Folders: []string{""}}
	register(t, resealed, registration)
	if _, err := sync.CopyProjectFrom(ctx, registration, envsource.NewFixed("exec", map[variablestore.Cell]envsource.Value{cell("", "K"): value("v", "")})); err == nil {
		t.Fatal("CopyProjectFrom() without a key it can open succeeded, want it refused rather than versioned some weaker way")
	}
	if _, err := store.Get(ctx, scope, tierWide("", "K"), false); !errors.Is(err, variablestore.ErrNotFound) {
		t.Fatalf("K = %v, want nothing copied", err)
	}
	sealedAfter, err := store.KeyValues.Read(ctx, digestKeyAt(scope.Tier))
	if err != nil || sealedAfter.Revision != sealedBefore.Revision {
		t.Fatalf("the digest key record moved from %s to %s (%v), want it kept", sealedBefore.Revision, sealedAfter.Revision, err)
	}
}

func TestEachTierVersionsAValueUnderItsOwnKey(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	versions := map[environment.Tier]string{}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		scope := variablestore.Scope{Project: "shop", Tier: tier}
		copyAt(t, store, scope, 1, map[variablestore.Cell]envsource.Value{cell("", "K"): value("same", "s1@1")})
		versions[tier] = reveal(t, store, scope, tierWide("", "K")).Provenance.Version
	}
	if versions[environment.TierProduction] == versions[environment.TierPreview] {
		t.Fatalf("production and preview versioned one value as %q alike, want each tier keyed apart", versions[environment.TierPreview])
	}
}

func TestTheDigestKeyIsBoundToEveryProjectInItsTierAtTheEnvSourceBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key, err := base64.StdEncoding.DecodeString("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
	if err != nil {
		t.Fatal(err)
	}
	store := variablestore.Store{
		KeyValues: fake.NewKeyValues(),
		Cipher:    fake.NewCipherWithKeys(map[environment.Tier][]byte{environment.TierProduction: key}),
	}
	if _, err := store.KeyValues.Write(ctx, keyvalue.Entry{
		Key:   digestKeyAt(environment.TierProduction),
		Value: []byte(`{"sealed":"9F/RJPgNa4porS/1Q8ItSL/KGK95oGpw8VmDf5tE0cWEbE3WkZMbSiX44Znd7nOg83GJcnPO7ZR86eqg"}`),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := envsource.EnsureDigestKey(ctx, store, environment.TierProduction); err != nil {
		t.Fatalf("EnsureDigestKey() = %v, want the key sealed at */production/*/%%2F/envsource/digestkey/ to open: every value an env source copied is versioned under it", err)
	}
}
