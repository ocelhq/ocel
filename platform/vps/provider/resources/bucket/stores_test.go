package bucket

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

func storeOf(project string, tier environment.Tier, env string) resources.ProvisionRequest {
	return resources.ProvisionRequest{Ref: provider.StackRef{Project: project, Tier: tier, Name: naming.InfraStack(env)}}
}

func TestBucketsOfTwoEnvironmentsEachShapeTheirOwnStore(t *testing.T) {
	var stores Stores
	for _, in := range []resources.ProvisionRequest{
		storeOf("shop", environment.TierProduction, "production"),
		storeOf("shop", environment.TierPreview, "pr-1"),
		storeOf("blog", environment.TierProduction, "production"),
	} {
		spec := storeContainer(in)
		shaped, err := stores.shaped("uploads", spec)
		if err != nil {
			t.Fatalf("shaped(%s) = %v, want each store shaped by its own buckets", spec.Name, err)
		}
		if shaped.Name != spec.Name {
			t.Errorf("shaped(%s) = the store %s", spec.Name, shaped.Name)
		}
		if got := stores.shape(spec.Name); got == nil || got.Name != spec.Name {
			t.Errorf("shape(%s) = %+v, want the store its bucket shaped", spec.Name, got)
		}
	}
}

func TestTwoBucketsShapingOneStoreDifferentlyAreRefused(t *testing.T) {
	var stores Stores
	in := storeOf("shop", environment.TierProduction, "production")
	if _, err := stores.shaped("uploads", storeContainer(in)); err != nil {
		t.Fatal(err)
	}
	patched := storeContainer(in)
	patched.Env = map[string]string{"RUSTFS_REGION": "elsewhere"}
	if _, err := stores.shaped("avatars", patched); err == nil {
		t.Error("shaped() of a second bucket shaping the same store differently = nil, want it refused")
	}
}
