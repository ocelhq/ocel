package values_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

func TestAMirroredValueCarriesTheSourceItCameFromUntilOcelWritesOverIt(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	from := values.Provenance{EnvSource: "infisical:p/prod", Version: "7"}

	mirrored, err := store.Mirror(ctx, scope, at("API_KEY"), "from-infisical", from, 0)
	if err != nil {
		t.Fatal(err)
	}
	if mirrored.Version != 1 || mirrored.Provenance != from {
		t.Fatalf("Mirror() = %+v, want version 1 naming where it came from", mirrored)
	}
	listed, err := store.List(ctx, scope)
	if err != nil || len(listed) != 1 || listed[0].Provenance != from {
		t.Fatalf("List() = %+v, %v, want the provenance listed", listed, err)
	}
	revealed, err := store.Get(ctx, scope, at("API_KEY"), true)
	if err != nil || revealed.Plaintext != "from-infisical" {
		t.Fatalf("Get() = %q, %v", revealed.Plaintext, err)
	}

	newer := values.Provenance{EnvSource: "infisical:p/prod", Version: "8"}
	if _, err := store.Mirror(ctx, scope, at("API_KEY"), "read-before-the-first-write", newer, 0); !errors.Is(err, values.ErrStaleVersion) {
		t.Fatalf("Mirror() expecting an absent cell over a held one = %v, want ErrStaleVersion", err)
	}

	set, err := store.Set(ctx, scope, at("API_KEY"), "by-hand", nil)
	if err != nil {
		t.Fatal(err)
	}
	if set.Provenance != (values.Provenance{}) {
		t.Fatalf("Set() over a mirrored value = %+v, want it ocel's own", set)
	}
}
