package deploy

import (
	"errors"
	"strings"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/membrane"
)

func membraneManifest() *contractv1.Manifest {
	return &contractv1.Manifest{
		Resources: []*contractv1.ManifestResource{
			{
				LogicalName: "database--main",
				Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main"},
				Config:      &contractv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{}},
			},
			{
				LogicalName: "bucket--uploads",
				Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_BUCKET, Name: "uploads"},
				Config:      &contractv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}},
			},
		},
	}
}

func TestAppCrossesMembrane(t *testing.T) {
	t.Parallel()

	manifest := membraneManifest()
	manifest.Usages = []*contractv1.ManifestUsage{
		{App: "web", Resource: "bucket--uploads"},
		{App: "web", Resource: "database--main"},
		{App: "worker", Resource: "database--main"},
	}

	if !appCrossesMembrane(manifest, "web") {
		t.Error("an app that links a bucket reaches it through the membrane")
	}
	if appCrossesMembrane(manifest, "worker") {
		t.Error("an app that links only postgres reaches it directly, so it is told nothing about the membrane's state")
	}
	if appCrossesMembrane(manifest, "docs") {
		t.Error("an app that links nothing reaches nothing through the membrane")
	}
}

func membranePlan() providerkit.StackPlan {
	return providerkit.StackPlan{Resources: []providerkit.Resource{
		{Name: "database--main", Declared: "database--main", Type: providerkit.LinkPostgres},
		{Name: "bucket--uploads", Declared: "bucket--uploads", Type: providerkit.LinkBucket},
	}}
}

func TestCheckMembraneServices(t *testing.T) {
	t.Parallel()

	t.Run("this provider serves every type its plans reach through the membrane", func(t *testing.T) {
		t.Parallel()

		if err := checkMembraneServices(membranePlan(), membrane.Serves); err != nil {
			t.Fatalf("checkMembraneServices = %v, want nil", err)
		}
	})

	t.Run("a type served by no membrane fails by resource, type and provider", func(t *testing.T) {
		t.Parallel()

		err := checkMembraneServices(membranePlan(), func(linksv1.LinkType) bool { return false })

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

		plan := providerkit.StackPlan{App: &providerkit.AppPlan{
			App:    "web",
			Grants: []providerkit.Link{{Name: "uploads", Resource: "bucket--uploads", Type: providerkit.LinkBucket}},
		}}

		var missing *MissingMembraneError
		if err := checkMembraneServices(plan, func(linksv1.LinkType) bool { return false }); !errors.As(err, &missing) {
			t.Fatalf("checkMembraneServices = %v, want a *MissingMembraneError", err)
		}
		if missing.Resource != "bucket--uploads" {
			t.Errorf("Resource = %q, want the name the app declared it under", missing.Resource)
		}
	})

	t.Run("postgres goes direct, so no membrane is asked for it", func(t *testing.T) {
		t.Parallel()

		plan := providerkit.StackPlan{Resources: membranePlan().Resources[:1]}

		if err := checkMembraneServices(plan, func(linksv1.LinkType) bool { return false }); err != nil {
			t.Fatalf("checkMembraneServices = %v, want postgres to need no membrane service", err)
		}
	})
}

func TestCompletesUploads(t *testing.T) {
	t.Parallel()

	t.Run("a bucket of ours completes its own uploads", func(t *testing.T) {
		t.Parallel()

		if !completesUploads(membraneManifest()) {
			t.Error("completesUploads = false, want true for a bucket this deploy provisions")
		}
	})

	t.Run("postgres alone completes nothing", func(t *testing.T) {
		t.Parallel()

		manifest := &contractv1.Manifest{Resources: membraneManifest().GetResources()[:1]}

		if completesUploads(manifest) {
			t.Error("completesUploads = true, want false where no bucket is ours")
		}
	})

	t.Run("a linked bucket completes uploads of its own", func(t *testing.T) {
		t.Parallel()

		manifest := membraneManifest()
		manifest.Resources[1].Linked = true

		if completesUploads(manifest) {
			t.Error("completesUploads = true, want false for a bucket handed to us")
		}
	})
}
