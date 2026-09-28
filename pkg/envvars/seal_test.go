package envvars_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
)

const keySealedWith = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

func sealedWithFixtureKey(t *testing.T) *fake.Cipher {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(keySealedWith)
	if err != nil {
		t.Fatal(err)
	}
	return fake.NewCipherWithKeys(map[environment.Tier][]byte{environment.TierProduction: key})
}

func holding(t *testing.T, rows map[string]records.Name) *fake.Records {
	t.Helper()
	store := fake.NewRecords()
	for body, name := range rows {
		if _, err := store.Write(context.Background(), records.Record{Name: name, Bytes: []byte(body)}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestACellSealedUnderTheCoordinateItHadBeforeThePackageMovedStillOpens(t *testing.T) {
	t.Parallel()

	store := envvars.Store{
		Records: holding(t, map[string]records.Name{
			`{"version":1,"updatedAt":1790580978,"size":14,"sealed":"+VgAViEpXXIk4f9gdIkzfy1DSDSCljR35ULyG/f4QqTpm7DEoFaNMcm+"}`: {"values", "shop", "production", "cells", "%2Fweb", "STRIPE_API_KEY", "staging"},
		}),
		Cipher: sealedWithFixtureKey(t),
	}
	scope := envvars.Scope{Project: "shop", Tier: environment.TierProduction}
	at := envvars.Coordinate{Cell: envvars.Cell{Folder: "/web", Key: "STRIPE_API_KEY"}, Environment: "staging"}

	value, err := store.Get(context.Background(), scope, at, true)
	if err != nil || value.Plaintext != "sk_live_secret" {
		t.Fatalf("Get() = %q, %v, want the value sealed at shop/production/staging/%%2Fweb//STRIPE_API_KEY/ to open: every stored cell is bound to those bytes", value.Plaintext, err)
	}
}

func TestABindingSealedUnderTheCoordinateItHadBeforeThePackageMovedStillOpens(t *testing.T) {
	t.Parallel()

	store := envvars.Store{
		Records: holding(t, map[string]records.Name{
			`{"version":1,"updatedAt":1790580978,"record":"eyJuYW1lIjoib3JkZXJzIn0=","owner":"ocel"}`:               {"values", "shop", "production", "bindings", "orders", "records", "*"},
			`{"version":1,"sealed":"kH20mriYvg2PFri7a1XPVAmM0y9YqXkDfsjQMCWX6gyemVuOD8Ro1RkBDEdPnJpRyqOs/YGNHQ=="}`: {"values", "shop", "production", "bindings", "orders", "values", "*"},
		}),
		Cipher: sealedWithFixtureKey(t),
	}
	scope := envvars.Scope{Project: "shop", Tier: environment.TierProduction}

	resolved, err := store.ResolveBinding(context.Background(), scope, "", "orders")
	if err != nil || string(resolved.Value) != `{"url":"postgres://orders"}` {
		t.Fatalf("ResolveBinding() = %q, %v, want the value sealed at shop/production/*/%%2F/orders/PROPERTIES/ to open: every stored binding is bound to those bytes", resolved.Value, err)
	}
}
