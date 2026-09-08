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

func repositoryHeld(t *testing.T, p *gcp.Provider, class providerkit.Class) *artifactregistry.Repository {
	t.Helper()

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
	return held
}

func TestProjectTheImageRepositoryStandsWhereTheDeployPushesTo(t *testing.T) {
	p := againstAProject(t)
	class := providerkit.ClassProduction
	bootstrapped(t, p, class)

	held := repositoryHeld(t, p, class)
	if held.Format != "DOCKER" {
		t.Errorf("the repository holds %s packages, want DOCKER: Cloud Run runs container images", held.Format)
	}
	if held.Mode != "STANDARD_REPOSITORY" {
		t.Errorf("the repository stands in %q mode, want STANDARD_REPOSITORY: an org holding disallowUnspecifiedMode refuses to create one under no mode at all", held.Mode)
	}
	if len(held.CleanupPolicies) != 1 {
		t.Fatalf("the repository stands under %d cleanup policies, want the one that prunes untagged versions: %+v", len(held.CleanupPolicies), held.CleanupPolicies)
	}
	policy, named := held.CleanupPolicies["drop-untagged"]
	if !named {
		t.Fatalf("the repository stands under %+v, want a drop-untagged policy: a repository nothing prunes grows without end", held.CleanupPolicies)
	}
	if policy.Action != "DELETE" || policy.Condition == nil ||
		policy.Condition.TagState != "UNTAGGED" || policy.Condition.OlderThan != "604800s" {
		t.Errorf("the drop-untagged policy is %+v, want DELETE on untagged versions older than a week: anything sooner takes the child manifests of a push still in flight", policy)
	}
}

func TestProjectARepositoryWhoseCleanupPolicyWasEditedAwayIsMendedByTheNextBootstrap(t *testing.T) {
	p := againstAProject(t)
	class := providerkit.ClassProduction
	bootstrapper := bootstrapped(t, p, class)

	ctx := context.Background()
	service, err := artifactregistry.NewService(ctx)
	if err != nil {
		t.Fatalf("reach Artifact Registry: %v", err)
	}
	name := "projects/" + liveProject() + "/locations/" + liveRegion() + "/repositories/" + p.Names().Repository(class)
	if _, err := service.Projects.Locations.Repositories.Patch(name, &artifactregistry.Repository{}).
		UpdateMask("cleanup_policies").Context(ctx).Do(); err != nil {
		t.Fatalf("take the cleanup policies off %s: %v", name, err)
	}

	plan, err := bootstrapper.Plan(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"})
	if err != nil {
		t.Fatalf("Plan(%s) = %v", class, err)
	}
	mending := false
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			if change.Kind == "artifactregistry:repository" && change.Action == providerkit.ActionUpdate {
				mending = true
			}
		}
	}
	if !mending {
		t.Errorf("Plan() after the policies were edited away shows %+v, want the repository row reading as an update: a survey that only asks whether a repository exists never mends one", plan.Groups)
	}

	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply(%s) = %v", class, err)
	}
	if _, named := repositoryHeld(t, p, class).CleanupPolicies["drop-untagged"]; !named {
		t.Error("the repository stands under no drop-untagged policy after a second bootstrap, and drift nothing mends is drift that stays")
	}
}
