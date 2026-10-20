package assets

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestAKeyOfNoKindOcelStoresIsRefusedBeforeTheBucketIsReached(t *testing.T) {
	for _, key := range []string{"prod/shop/web/r1a2b3c4d/isr/cache/index.cache.json", "cache/shop/web/prod/r1a2b3c4d/x", ""} {
		_, err := Store{}.Has(context.Background(), provider.ArtifactRef{Tier: environment.TierProduction, Key: key})
		if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
			t.Errorf("Has(%q) = %v, want an invalid-input refusal", key, err)
		}
	}
}

func TestRemovingObjectsOfAScopeNamingNoProjectIsRefused(t *testing.T) {
	err := Store{}.RemoveObjects(context.Background(), environment.TierProduction, naming.StorageScope{}, naming.CoordinateObjectKinds, nil)
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
		t.Errorf("RemoveObjects() of a scope naming no project = %v, want an invalid-input refusal", err)
	}
}
