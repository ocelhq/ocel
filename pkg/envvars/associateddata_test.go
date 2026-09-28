package envvars_test

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
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

var shop = envvars.ValuesPartition(envvars.Scope{Project: "shop", Tier: environment.TierProduction})

func newKeyValuesHolding(t *testing.T, rows map[string]keyvalue.Key) *fake.KeyValues {
	t.Helper()
	store := fake.NewKeyValues()
	for body, name := range rows {
		if _, err := store.Write(context.Background(), keyvalue.Entry{Key: name, Value: []byte(body)}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestACellsValueIsBoundToItsProjectClassEnvironmentFolderEmptyBindingAndKeyInThatOrder(t *testing.T) {
	t.Parallel()

	store := envvars.Store{
		KeyValues: newKeyValuesHolding(t, map[string]keyvalue.Key{
			`{"version":1,"updatedAt":1790580978,"size":14,"sealed":"+VgAViEpXXIk4f9gdIkzfy1DSDSCljR35ULyG/f4QqTpm7DEoFaNMcm+"}`: shop.Key("cells", "/web", "STRIPE_API_KEY", "staging"),
		}),
		Cipher: newFixtureKeyCipher(t),
	}
	scope := envvars.Scope{Project: "shop", Tier: environment.TierProduction}
	at := envvars.Coordinate{Cell: envvars.Cell{Folder: "/web", Key: "STRIPE_API_KEY"}, Environment: "staging"}

	value, err := store.Get(context.Background(), scope, at, true)
	if err != nil || value.Plaintext != "sk_live_secret" {
		t.Fatalf("Get() = %q, %v, want the value sealed at shop/production/staging/%%2Fweb//STRIPE_API_KEY/ to open: every stored cell is bound to those bytes", value.Plaintext, err)
	}
}

func TestABindingsValueIsBoundToTheRootFolderItsNameAndThePropertiesKey(t *testing.T) {
	t.Parallel()

	store := envvars.Store{
		KeyValues: newKeyValuesHolding(t, map[string]keyvalue.Key{
			`{"version":1,"updatedAt":1790580978,"record":"eyJuYW1lIjoib3JkZXJzIn0=","owner":"ocel"}`:               shop.Key("bindings", "orders", "records", "*"),
			`{"version":1,"sealed":"kH20mriYvg2PFri7a1XPVAmM0y9YqXkDfsjQMCWX6gyemVuOD8Ro1RkBDEdPnJpRyqOs/YGNHQ=="}`: shop.Key("bindings", "orders", "values", "*"),
		}),
		Cipher: newFixtureKeyCipher(t),
	}
	scope := envvars.Scope{Project: "shop", Tier: environment.TierProduction}

	resolved, err := store.ResolveBinding(context.Background(), scope, "", "orders")
	if err != nil || string(resolved.Value) != `{"url":"postgres://orders"}` {
		t.Fatalf("ResolveBinding() = %q, %v, want the value sealed at shop/production/*/%%2F/orders/PROPERTIES/ to open: every stored binding is bound to those bytes", resolved.Value, err)
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

	for name, write := range map[string]func(envvars.Store) error{
		"a cell with no project": func(store envvars.Store) error {
			_, err := store.Set(context.Background(), envvars.Scope{Tier: environment.TierProduction}, at("KEY"), "value", nil)
			return err
		},
		"a cell with no key": func(store envvars.Store) error {
			_, err := store.Set(context.Background(), envvars.Scope{Project: "shop", Tier: environment.TierProduction}, at(""), "value", nil)
			return err
		},
		"a binding with no project": func(store envvars.Store) error {
			_, err := store.SetBinding(context.Background(), envvars.Scope{Tier: environment.TierProduction}, "", envvars.OwnerOcel, "orders",
				envvars.BindingWrite{Record: []byte(`{}`), Value: []byte(`{}`)})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cipher := &countedCipher{Cipher: fake.NewCipher()}
			store := envvars.Store{KeyValues: fake.NewKeyValues(), Cipher: cipher}

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
	store := envvars.Store{KeyValues: fake.NewKeyValues(), Cipher: cipher}

	_, err := store.SetBinding(context.Background(), envvars.Scope{Project: "shop", Tier: environment.TierProduction}, "", envvars.OwnerOcel, "",
		envvars.BindingWrite{Record: []byte(`{}`), Value: []byte(`{}`)})
	if err == nil {
		t.Fatal("SetBinding() with no name = nil, want a refusal: a binding with no name is bound to the bytes of the root-folder PROPERTIES cell")
	}
	if cipher.sealed != 0 {
		t.Fatalf("the cipher sealed %d value(s) before the write was refused", cipher.sealed)
	}
}
