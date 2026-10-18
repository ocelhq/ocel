package variablestore_test

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

const keySealedWith = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

func newFixtureKeyCipher(t *testing.T) *fake.Cipher {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(keySealedWith)
	if err != nil {
		t.Fatal(err)
	}
	return fake.NewCipherWithKeys(map[environment.Tier][]byte{environment.TierProduction: key})
}

var shop = variablestore.ValuesPartition(variablestore.Scope{Project: "shop", Tier: environment.TierProduction})

func newKeyValuesHolding(t *testing.T, rows map[string]records.Key) *fake.KeyValues {
	t.Helper()
	store := fake.NewKeyValues()
	for body, name := range rows {
		if _, err := store.Write(context.Background(), records.Entry{Key: name, Value: []byte(body)}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestACellsValueIsBoundAsAVariableToItsProjectTierEnvironmentFolderAndKey(t *testing.T) {
	t.Parallel()

	store := variablestore.Store{
		KeyValues: newKeyValuesHolding(t, map[string]records.Key{
			`{"version":1,"updatedAt":1790580978,"size":14,"sealed":"+qO3M5S8MwOK2Gkpow0p88BOvmOIcGhcaD8yDkAqDwYyR/53Ng/XZnxn"}`: shop.Key("cells", "/web", "STRIPE_API_KEY", "staging"),
		}),
		Cipher: newFixtureKeyCipher(t),
	}
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}
	at := variablestore.Coordinate{Cell: variablestore.Cell{Folder: "/web", Key: "STRIPE_API_KEY"}, Environment: "staging"}

	value, err := store.Get(context.Background(), scope, at, true)
	if err != nil || value.Plaintext != "sk_live_secret" {
		t.Fatalf("Get() = %q, %v, want the value sealed as a variable of shop at production/staging//web/STRIPE_API_KEY to open: every stored cell is bound to those", value.Plaintext, err)
	}
}

func TestABindingsValueIsBoundAsABindingToItsProjectTierEnvironmentAndName(t *testing.T) {
	t.Parallel()

	store := variablestore.Store{
		KeyValues: newKeyValuesHolding(t, map[string]records.Key{
			`{"version":1,"updatedAt":1790580978,"record":"eyJuYW1lIjoib3JkZXJzIn0=","owner":"ocel"}`:               shop.Key("bindings", "orders", "records", "*"),
			`{"version":1,"sealed":"Gr+tqkmj7M4Aef7sQCz70w/KupM/W3cuKa69Nk3UwZkN4xfKIaAj+IC+pkJ2dkFP3YYcTea8cw=="}`: shop.Key("bindings", "orders", "values", "*"),
		}),
		Cipher: newFixtureKeyCipher(t),
	}
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}

	resolved, err := store.ResolveBinding(context.Background(), scope, "", "orders")
	if err != nil || string(resolved.Value) != `{"url":"postgres://orders"}` {
		t.Fatalf("ResolveBinding() = %q, %v, want the value sealed as a binding of shop at production/*/orders to open: every stored binding is bound to those", resolved.Value, err)
	}
}

type countedCipher struct {
	*fake.Cipher
	sealed int
}

func (c *countedCipher) Seal(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
	c.sealed++
	return c.Cipher.Seal(ctx, tier, bound, plaintext)
}

func TestAValueThatNamesNoProjectOrKeyIsRefusedBeforeItIsSealed(t *testing.T) {
	t.Parallel()

	for name, write := range map[string]func(variablestore.Store) error{
		"a cell with no project": func(store variablestore.Store) error {
			_, err := store.Set(context.Background(), variablestore.Scope{Tier: environment.TierProduction}, at("KEY"), "value", nil)
			return err
		},
		"a cell with no key": func(store variablestore.Store) error {
			_, err := store.Set(context.Background(), variablestore.Scope{Project: "shop", Tier: environment.TierProduction}, at(""), "value", nil)
			return err
		},
		"a binding with no project": func(store variablestore.Store) error {
			_, err := store.SetBinding(context.Background(), variablestore.Scope{Tier: environment.TierProduction}, "", variablestore.OwnerOcel, "orders",
				variablestore.BindingWrite{Record: []byte(`{}`), Value: []byte(`{}`)})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cipher := &countedCipher{Cipher: fake.NewCipher()}
			store := variablestore.Store{KeyValues: fake.NewKeyValues(), Cipher: cipher}

			err := write(store)
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Fatalf("write = %v, want an invalid refusal: a value bound to an empty project or key shares its binding with every other value missing it", err)
			}
			if cipher.sealed != 0 {
				t.Fatalf("the cipher sealed %d value(s) before the write was refused", cipher.sealed)
			}
		})
	}
}

func TestABindingThatNamesNoBindingIsRefusedBeforeItIsSealed(t *testing.T) {
	t.Parallel()

	cipher := &countedCipher{Cipher: fake.NewCipher()}
	store := variablestore.Store{KeyValues: fake.NewKeyValues(), Cipher: cipher}

	_, err := store.SetBinding(context.Background(), variablestore.Scope{Project: "shop", Tier: environment.TierProduction}, "", variablestore.OwnerOcel, "",
		variablestore.BindingWrite{Record: []byte(`{}`), Value: []byte(`{}`)})
	if err == nil {
		t.Fatal("SetBinding() with no name = nil, want a refusal: a binding with no name names nothing to seal its value to")
	}
	if cipher.sealed != 0 {
		t.Fatalf("the cipher sealed %d value(s) before the write was refused", cipher.sealed)
	}
}
