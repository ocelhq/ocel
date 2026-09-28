package live

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestAStoreSecretIsBoundToItsProjectTierStackAndTheStoreKeyUnderResources(t *testing.T) {
	t.Parallel()
	key, err := base64.StdEncoding.DecodeString(keySealedWith)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := base64.StdEncoding.DecodeString("1F1t2UqIf00/MBsBS1X51mG2eSXf7ufZbcsyacsKGbgLgT7aCPgaKw==")
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Slug: "shop", Tier: "production", Store: &Store{Env: "shop-prod"}}

	bound, err := NewStoreSecretAssociatedData(manifest.Slug, environment.Tier(manifest.Tier), manifest.Store.Env)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(key, bound, sealed)
	if err != nil || string(opened) != "s3cr3t-store" {
		t.Fatalf("Open() = %q, %v, want the store secret sealed at shop/production/shop-prod/resources/store/storekey/ to open: every box's store secret is bound to those bytes", opened, err)
	}
}

func TestASecretThatNamesNoProjectOrStackIsRefusedRatherThanBoundToTheEmptyString(t *testing.T) {
	t.Parallel()

	for name, secret := range map[string]struct{ project, stack string }{
		"no project": {project: "", stack: "prod--infra"},
		"no stack":   {project: "shop", stack: ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := NewSecretAssociatedData(secret.project, environment.TierProduction, secret.stack, "resources", "main", "password")
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Fatalf("NewSecretAssociatedData() = %v, want an invalid refusal: a secret bound to an empty project or stack opens for every caller missing it", err)
			}
		})
	}
}
