package deploy

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func runtimeResources() []provider.Resource {
	return []provider.Resource{
		{Name: "database--main", Declared: "database--main", Type: provider.BindingPostgres},
		{Name: "bucket--uploads", Declared: "bucket--uploads", Type: provider.BindingBucket},
	}
}

func runtimeSpec() provider.StackSpec {
	return provider.StackSpec{Resources: runtimeResources()}
}

func TestProvisionsBucket(t *testing.T) {
	t.Parallel()

	t.Run("a bucket of ours completes its own uploads", func(t *testing.T) {
		t.Parallel()

		if !provisionsBucket(runtimeSpec()) {
			t.Error("provisionsBucket = false, want true for a bucket this deploy provisions")
		}
	})

	t.Run("postgres alone completes nothing", func(t *testing.T) {
		t.Parallel()

		spec := runtimeSpec()
		spec.Resources = spec.Resources[:1]

		if provisionsBucket(spec) {
			t.Error("provisionsBucket = true, want false where no bucket is ours")
		}
	})

	t.Run("a bound bucket completes uploads of its own", func(t *testing.T) {
		t.Parallel()

		spec := runtimeSpec()
		spec.Resources[1].Binding = spec.Resources[1].Name

		if provisionsBucket(spec) {
			t.Error("provisionsBucket = true, want false for a bucket handed to us")
		}
	})
}
