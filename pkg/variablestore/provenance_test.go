package variablestore_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/variablestore"
)

func TestForgettingWhatOneEnvSourceReadCopiedLeavesNothingOfItAndKeepsAnotherReadsCopy(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	stale := variablestore.Provenance{EnvSource: "infisical:p/prod", Version: "s1@1", ReadAt: time.Unix(1_800_000_000, 0)}
	later := variablestore.Provenance{EnvSource: "infisical:p/prod", Version: "s2@1", ReadAt: stale.ReadAt.Add(time.Minute)}
	if _, err := store.SetFromEnvSource(ctx, scope, at("API_KEY"), "stale", stale, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFromEnvSource(ctx, scope, at("TOKEN"), "later", later, 0); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"API_KEY", "TOKEN", "NEVER_SET"} {
		if err := store.ForgetFromEnvSource(ctx, scope, at(key), stale); err != nil {
			t.Fatalf("ForgetFromEnvSource(%s) = %v", key, err)
		}
	}

	left, err := store.KeyValues.List(ctx, variablestore.ValuesPartition(scope))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range left {
		if slices.Contains(entry.Key.Path, "API_KEY") {
			t.Errorf("%s left behind by forgetting the read that copied it", entry.Key)
		}
	}
	if token, err := store.Get(ctx, scope, at("TOKEN"), true); err != nil || token.Plaintext != "later" {
		t.Fatalf("TOKEN = %+v, %v, want the later read's copy kept", token, err)
	}
}

func TestAValueCopiedFromAnEnvSourceNamesItUntilOcelWritesOverIt(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	from := variablestore.Provenance{EnvSource: "infisical:p/prod", Version: "s1@7"}

	copied, err := store.SetFromEnvSource(ctx, scope, at("API_KEY"), "from-infisical", from, 0)
	if err != nil {
		t.Fatal(err)
	}
	if copied.Version != 1 || copied.Provenance != from {
		t.Fatalf("SetFromEnvSource() = %+v, want version 1 naming where it came from", copied)
	}
	listed, err := store.List(ctx, scope)
	if err != nil || len(listed) != 1 || listed[0].Provenance != from {
		t.Fatalf("List() = %+v, %v, want the provenance listed", listed, err)
	}
	revealed, err := store.Get(ctx, scope, at("API_KEY"), true)
	if err != nil || revealed.Plaintext != "from-infisical" || revealed.Provenance != from {
		t.Fatalf("Get() = %+v, %v", revealed, err)
	}

	newer := variablestore.Provenance{EnvSource: "infisical:p/prod", Version: "s1@8"}
	if _, err := store.SetFromEnvSource(ctx, scope, at("API_KEY"), "read-before-the-first-write", newer, 0); !errors.Is(err, variablestore.ErrStaleVersion) {
		t.Fatalf("SetFromEnvSource() expecting an absent value over a stored one = %v, want ErrStaleVersion", err)
	}

	set, err := store.Set(ctx, scope, at("API_KEY"), "by-hand", nil)
	if err != nil {
		t.Fatal(err)
	}
	if set.Provenance != (variablestore.Provenance{}) {
		t.Fatalf("Set() over a copied value = %+v, want it ocel's own", set)
	}
}

func TestADereferencedValueNamesTheProjectAndProvenanceItLandsOn(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	shared := variablestore.Scope{Project: "shared", Tier: scope.Tier}
	from := variablestore.Provenance{EnvSource: "infisical:p/prod", Version: "s1@1"}
	if _, err := store.SetFromEnvSource(ctx, shared, at("TOKEN"), "shared-token", from, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReference(ctx, scope, at("TOKEN"), variablestore.Target{Project: "shared", Cell: variablestore.Cell{Key: "TOKEN"}}); err != nil {
		t.Fatal(err)
	}

	hidden, err := store.GetDereferenced(ctx, scope, at("TOKEN"), false)
	if err != nil {
		t.Fatal(err)
	}
	if hidden.Project != "shared" || hidden.Coordinate.Key != "TOKEN" || hidden.Provenance != from || hidden.Plaintext != "" {
		t.Fatalf("GetDereferenced() without reveal = %+v, want shared's TOKEN and no plaintext", hidden)
	}
	revealed, err := store.GetDereferenced(ctx, scope, at("TOKEN"), true)
	if err != nil || revealed.Plaintext != "shared-token" {
		t.Fatalf("GetDereferenced() with reveal = %+v, %v", revealed, err)
	}

	if _, err := store.Set(ctx, scope, at("OWN"), "mine", nil); err != nil {
		t.Fatal(err)
	}
	own, err := store.GetDereferenced(ctx, scope, at("OWN"), true)
	if err != nil || own.Project != "shop" || own.Plaintext != "mine" {
		t.Fatalf("GetDereferenced() of a plain value = %+v, %v, want it in its own project", own, err)
	}

	if _, err := store.GetDereferenced(ctx, scope, at("ABSENT"), false); !errors.Is(err, variablestore.ErrNotFound) {
		t.Fatalf("GetDereferenced() of nothing = %v, want ErrNotFound", err)
	}
	if _, err := store.Delete(ctx, shared, at("TOKEN"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetDereferenced(ctx, scope, at("TOKEN"), false); !errors.Is(err, variablestore.ErrDangling) {
		t.Fatalf("GetDereferenced() through a dangling reference = %v, want ErrDangling", err)
	}
}
