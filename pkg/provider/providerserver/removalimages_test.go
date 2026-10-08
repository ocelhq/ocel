package providerserver_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

var projectRegistry = &contractv1.ImageRegistry{Server: "registry.example.com", Namespace: "acme", Username: "acme-bot", Password: "hunter2"}

func TestRemoveProjectHandsEveryDestroyTheStoreOfTheRegistryTheProjectNames(t *testing.T) {
	client, vendor := deployedProject(t)
	req := projectRequest()
	req.ProjectRegistry = projectRegistry

	stream, err := client.RemoveProject(context.Background(), req)
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	stores := vendor.FakeStacks().DestroyedWith()
	if len(stores) == 0 {
		t.Fatal("RemoveProject() destroyed no stack")
	}
	for _, store := range stores {
		if store != provider.ImageStore(vendor.ImageStore()) {
			t.Errorf("a stack was destroyed with %v, want the store opened on the project's registry: the images its deploys pushed there are deleted through it", store)
		}
	}
	opened := vendor.ImageStore().Opened()
	if len(opened) != 1 || opened[0].Server != "registry.example.com" || opened[0].Password != "hunter2" {
		t.Errorf("the removal opened %v, want the project's registry with the credentials the CLI sent, once", opened)
	}
}

func TestRemoveProjectWithoutARegistryOpensNoStore(t *testing.T) {
	client, vendor := deployedProject(t)

	stream, err := client.RemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	for _, store := range vendor.FakeStacks().DestroyedWith() {
		if store != nil {
			t.Errorf("a stack was destroyed with %v, want none: no registry was named", store)
		}
	}
	if opened := vendor.ImageStore().Opened(); len(opened) != 0 {
		t.Errorf("the removal opened %v, want no registry", opened)
	}
}

func TestRemoveEnvironmentHandsEveryDestroyTheStoreOfTheRegistryTheProjectNames(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	seedPromotions(t, vendor, environment.TierPreview, "shop", "pr-7", "p1", "p2")
	outlived := naming.AppStack("pr-7", "web", releaseOf(t, releaseFor(7)))
	seedEnvironment(t, vendor, "shop", outlived, naming.InfraStack("pr-7"))

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug:            "shop",
		Environment:     &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7"},
		ProjectRegistry: projectRegistry,
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q, %v", result.GetError(), err)
	}

	stores := vendor.FakeStacks().DestroyedWith()
	if len(stores) == 0 {
		t.Fatal("RemoveEnvironment() destroyed no stack")
	}
	for _, store := range stores {
		if store != provider.ImageStore(vendor.ImageStore()) {
			t.Errorf("a stack was destroyed with %v, want the store opened on the project's registry", store)
		}
	}
}

func TestADeployThatReclaimsADroppedBuildHandsTheDestroyTheStoreItPushedTo(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	seedKept(t, vendor)
	req := deployRequest()
	req.ProjectRegistry = projectRegistry

	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	stores := vendor.FakeStacks().DestroyedWith()
	if len(stores) == 0 {
		t.Fatal("the deploy destroyed no stack, and this case drops a build past the retained promotions")
	}
	for _, store := range stores {
		if store != provider.ImageStore(vendor.ImageStore()) {
			t.Errorf("a dropped build was destroyed with %v, want the store the deploy pushed to: it is where that build's image lives", store)
		}
	}
}
