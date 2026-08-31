package deploy

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func membraneResources() []providerkit.Resource {
	return []providerkit.Resource{
		{Name: "database--main", Declared: "database--main", Type: providerkit.LinkPostgres},
		{Name: "bucket--uploads", Declared: "bucket--uploads", Type: providerkit.LinkBucket},
	}
}

func membranePlan() providerkit.StackPlan {
	return providerkit.StackPlan{Resources: membraneResources()}
}

func TestProvisionsBucket(t *testing.T) {
	t.Parallel()

	t.Run("a bucket of ours completes its own uploads", func(t *testing.T) {
		t.Parallel()

		if !provisionsBucket(membranePlan()) {
			t.Error("provisionsBucket = false, want true for a bucket this deploy provisions")
		}
	})

	t.Run("postgres alone completes nothing", func(t *testing.T) {
		t.Parallel()

		plan := membranePlan()
		plan.Resources = plan.Resources[:1]

		if provisionsBucket(plan) {
			t.Error("provisionsBucket = true, want false where no bucket is ours")
		}
	})

	t.Run("a linked bucket completes uploads of its own", func(t *testing.T) {
		t.Parallel()

		plan := membranePlan()
		plan.Resources[1].Linked = true

		if provisionsBucket(plan) {
			t.Error("provisionsBucket = true, want false for a bucket handed to us")
		}
	})
}
