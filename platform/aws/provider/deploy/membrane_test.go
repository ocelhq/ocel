package deploy

import (
	"errors"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/membrane"
)

func membraneManifest() *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		Resources: []*deploymentsv1.ManifestResource{
			{
				LogicalName: "database--main",
				Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main"},
				Config:      &deploymentsv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{}},
			},
			{
				LogicalName: "bucket--uploads",
				Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_BUCKET, Name: "uploads"},
				Config:      &deploymentsv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}},
			},
		},
	}
}

func TestAppCrossesMembrane(t *testing.T) {
	t.Parallel()

	manifest := membraneManifest()
	manifest.Usages = []*deploymentsv1.ManifestUsage{
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

func TestCheckMembraneServices(t *testing.T) {
	t.Parallel()

	t.Run("this provider serves every type its manifests reach through the membrane", func(t *testing.T) {
		t.Parallel()

		if err := checkMembraneServices(membraneManifest(), membrane.Serves); err != nil {
			t.Fatalf("checkMembraneServices = %v, want nil", err)
		}
	})

	t.Run("a type served by no membrane fails by resource, type and provider", func(t *testing.T) {
		t.Parallel()

		err := checkMembraneServices(membraneManifest(), func(linksv1.LinkType) bool { return false })

		var missing *MissingMembraneError
		if !errors.As(err, &missing) {
			t.Fatalf("checkMembraneServices = %v, want a *MissingMembraneError", err)
		}
		for _, want := range []string{"bucket--uploads", linksv1.LinkType_LINK_TYPE_BUCKET.String(), providerName} {
			if !strings.Contains(missing.Error(), want) {
				t.Errorf("Error() = %q, missing %q", missing.Error(), want)
			}
		}
	})

	t.Run("postgres goes direct, so no membrane is asked for it", func(t *testing.T) {
		t.Parallel()

		manifest := &deploymentsv1.Manifest{Resources: membraneManifest().GetResources()[:1]}

		if err := checkMembraneServices(manifest, func(linksv1.LinkType) bool { return false }); err != nil {
			t.Fatalf("checkMembraneServices = %v, want postgres to need no membrane service", err)
		}
	})
}

func TestCheckListenerCode(t *testing.T) {
	t.Parallel()

	t.Run("a bucket with no listener code fails before any cloud call", func(t *testing.T) {
		t.Parallel()

		err := checkListenerCode(membraneManifest(), "")

		var missing *MissingListenerCodeError
		if !errors.As(err, &missing) {
			t.Fatalf("checkListenerCode = %v, want a *MissingListenerCodeError", err)
		}
		for _, want := range []string{"bucket--uploads", listenerCodePathEnvVar} {
			if !strings.Contains(missing.Error(), want) {
				t.Errorf("Error() = %q, missing %q", missing.Error(), want)
			}
		}
	})

	t.Run("a shipped listener passes", func(t *testing.T) {
		t.Parallel()

		if err := checkListenerCode(membraneManifest(), "dist/ocel-listener.zip"); err != nil {
			t.Fatalf("checkListenerCode = %v, want nil", err)
		}
	})

	t.Run("postgres alone needs no listener", func(t *testing.T) {
		t.Parallel()

		manifest := &deploymentsv1.Manifest{Resources: membraneManifest().GetResources()[:1]}

		if err := checkListenerCode(manifest, ""); err != nil {
			t.Fatalf("checkListenerCode = %v, want nil", err)
		}
	})

	t.Run("a linked bucket ships no listener of ours", func(t *testing.T) {
		t.Parallel()

		manifest := membraneManifest()
		manifest.Resources[1].Linked = true

		if err := checkListenerCode(manifest, ""); err != nil {
			t.Fatalf("checkListenerCode = %v, want nil", err)
		}
	})
}
