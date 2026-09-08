package gcp_test

import (
	"context"
	"os"
	"testing"

	"google.golang.org/api/artifactregistry/v1"

	"github.com/ocelhq/ocel/pkg/providerkit"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

func againstAProject(t *testing.T) *gcp.Provider {
	t.Helper()
	if emulated() || os.Getenv(liveProjectVariable) == "" {
		t.Skipf("Artifact Registry is served by Google alone, so this runs against a real project: name one in %s", liveProjectVariable)
	}
	return newProvider(t, gcp.Options{Project: liveProject(), Region: liveRegion()})
}

func TestProjectTheImageRepositoryStandsWhereTheDeployPushesTo(t *testing.T) {
	p := againstAProject(t)
	class := providerkit.ClassProduction
	bootstrapped(t, p, class)

	ctx := context.Background()
	service, err := artifactregistry.NewService(ctx)
	if err != nil {
		t.Fatalf("reach Artifact Registry: %v", err)
	}
	name := "projects/" + liveProject() + "/locations/" + liveRegion() + "/repositories/" + p.Names().Repository(class)
	held, err := service.Projects.Locations.Repositories.Get(name).Context(ctx).Do()
	if err != nil {
		t.Fatalf("Get(%s) after a bootstrap = %v, want the repository the deploy pushes images to", name, err)
	}
	if held.Format != "DOCKER" {
		t.Errorf("the repository holds %s packages, want DOCKER: Cloud Run runs container images", held.Format)
	}
	kept := 0
	for _, policy := range held.CleanupPolicies {
		if policy.MostRecentVersions != nil {
			kept = int(policy.MostRecentVersions.KeepCount)
		}
	}
	if kept == 0 {
		t.Error("the repository keeps every version ever pushed, and a repository nothing prunes grows without end")
	}
}
