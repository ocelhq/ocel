package variablestore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/variablestore"
)

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
