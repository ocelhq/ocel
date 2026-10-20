package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestADeclaredBucketCarriesItsTiersTagOnceHoweverOftenItIsProvisioned(t *testing.T) {
	ctx := context.Background()
	p, _ := servingStorage(t)
	tags := googletest.NewTieredTags(t)
	tags.Tagged(p.resolved)

	var name string
	for range 2 {
		binding, err := p.bucketHooks().Provision(ctx, aBucket(provider.BucketSpec{}), nil)
		if err != nil {
			t.Fatal(err)
		}
		name = binding.Properties[provider.PropertyBucket]
	}
	if got := tags.Bound(p.resolved.BucketTagged(name).Name); !slices.Equal(got, []string{tags.ValueOf(environment.TierProduction)}) {
		t.Errorf("the declared bucket %s carries %v, want the production value once", name, got)
	}
}
