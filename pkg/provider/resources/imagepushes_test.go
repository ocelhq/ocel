package resources_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

func TestAnImageRemovedWhileItsReleaseProvisionedIsPushedAgainAndSaidSo(t *testing.T) {
	t.Parallel()

	const pushed = "registry.invalid/shop/web:sha256-abc"
	images := fake.NewImages()
	stacks := resources.NewHookStacks(fake.NewKeyValues(), resources.Hooks{
		Containers: &resources.ContainerHooks{
			Provision: func(ctx context.Context, spec provider.StackSpec, _ progress.Log) ([]provider.AppContainer, error) {
				if err := images.Remove(ctx, pushed); err != nil {
					return nil, err
				}
				return []provider.AppContainer{{Name: spec.App.App, Image: pushed}}, nil
			},
			Remove: func(context.Context, provider.StackRef, []provider.AppContainer, provider.ImageStore, progress.Log) error {
				return nil
			},
		},
	})
	said := &fake.Log{}
	spec := provider.StackSpec{
		Ref:  appRef(),
		Kind: provider.StackApp,
		App:  containerApp("web"),
		Images: provider.ImagePushes{Store: images, Pushes: []provider.ImagePush{
			{App: "web", Source: testImage, ImageRef: pushed},
		}},
	}
	if _, err := stacks.Provision(context.Background(), spec, said); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if !images.Stored(pushed) {
		t.Errorf("the registry no longer holds %s after the release provisioned: the release's next task start or cold pull fails with nothing said", pushed)
	}
	if !strings.Contains(strings.Join(said.Lines(), "\n"), pushed) {
		t.Errorf("the release said %q, want it to warn that %s left the registry while it provisioned", said.Lines(), pushed)
	}
}
