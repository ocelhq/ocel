package deploy

import (
	"errors"
	"strings"
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

func TestCheckMembraneServices(t *testing.T) {
	t.Parallel()

	t.Run("this provider serves every type its plans reach through the membrane", func(t *testing.T) {
		t.Parallel()

		if err := checkMembraneServices(membraneResources(), nil, membrane.Serves); err != nil {
			t.Fatalf("checkMembraneServices = %v, want nil", err)
		}
	})

	t.Run("a type served by no membrane fails by resource, type and provider", func(t *testing.T) {
		t.Parallel()

		err := checkMembraneServices(membraneResources(), nil, func(linksv1.LinkType) bool { return false })

		var missing *MissingMembraneError
		if !errors.As(err, &missing) {
			t.Fatalf("checkMembraneServices = %v, want a *MissingMembraneError", err)
		}
		for _, want := range []string{"bucket--uploads", string(providerkit.LinkBucket), providerName} {
			if !strings.Contains(missing.Error(), want) {
				t.Errorf("Error() = %q, missing %q", missing.Error(), want)
			}
		}
	})

	t.Run("an app is refused for a link it is granted, not only for one this deploy stands up", func(t *testing.T) {
		t.Parallel()

		grants := []providerkit.Link{{Name: "uploads", Resource: "bucket--uploads", Type: providerkit.LinkBucket}}

		var missing *MissingMembraneError
		if err := checkMembraneServices(nil, grants, func(linksv1.LinkType) bool { return false }); !errors.As(err, &missing) {
			t.Fatalf("checkMembraneServices = %v, want a *MissingMembraneError", err)
		}
		if missing.Resource != "bucket--uploads" {
			t.Errorf("Resource = %q, want the name the app declared it under", missing.Resource)
		}
	})

	t.Run("postgres goes direct, so no membrane is asked for it", func(t *testing.T) {
		t.Parallel()

		if err := checkMembraneServices(membraneResources()[:1], nil, func(linksv1.LinkType) bool { return false }); err != nil {
			t.Fatalf("checkMembraneServices = %v, want postgres to need no membrane service", err)
		}
	})
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
