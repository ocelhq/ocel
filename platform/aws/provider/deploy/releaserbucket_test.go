package deploy

import (
	"slices"
	"testing"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/membrane"
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

func TestEveryCrossingTypeThisProviderServesIsOneTheMembraneRuns(t *testing.T) {
	t.Parallel()

	var crossing []linksv1.LinkType
	for _, kind := range Serves() {
		if providerkit.CrossesMembrane(kind) {
			crossing = append(crossing, providerkit.WireLinkType(kind))
		}
	}
	var run []linksv1.LinkType
	for wire := range linksv1.LinkType_name {
		kind := linksv1.LinkType(wire)
		if membrane.Serves(kind) {
			run = append(run, kind)
		}
	}
	slices.Sort(crossing)
	slices.Sort(run)
	if !slices.Equal(crossing, run) {
		t.Errorf("this provider says it serves %v of the types an app reaches through the membrane and the membrane runs %v: preflight lets a deploy past on the first list, and the app meets the second one at its first call",
			crossing, run)
	}
}
