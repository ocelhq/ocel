package providerkit_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestTheCoordinateIsTheTargetPlusTheAppRepositoryAndTheDigestTag(t *testing.T) {
	target := providerkit.RegistryTarget{Server: "ghcr.io", Namespace: "acme/ocel"}

	if got, want := target.Coordinate("web", "sha256-abc"), "ghcr.io/acme/ocel/web:sha256-abc"; got != want {
		t.Errorf("Coordinate() = %q, want %q", got, want)
	}
}

func TestACoordinateUnderARegistryWithNoNamespaceSitsDirectlyOnTheServer(t *testing.T) {
	target := providerkit.RegistryTarget{Server: "registry.fly.io"}

	if got, want := target.Coordinate("web", "sha256-abc"), "registry.fly.io/web:sha256-abc"; got != want {
		t.Errorf("Coordinate() = %q, want %q", got, want)
	}
}

func TestAResolvedTargetRendersWithoutItsPassword(t *testing.T) {
	target := providerkit.RegistryTarget{Server: "ghcr.io", Namespace: "acme", Username: "acme-bot", Password: "ghp_livesecret"}

	for _, rendered := range []string{
		fmt.Sprintf("%v", target),
		fmt.Sprintf("%+v", target),
		fmt.Sprintf("target=%s", target),
		fmt.Sprintf("%#v", target),
		fmt.Errorf("push to %v: %w", target, errors.New("denied")).Error(),
	} {
		if strings.Contains(rendered, "ghp_livesecret") {
			t.Errorf("a registry target rendered as %q, and the password rides along into any log line that prints one", rendered)
		}
		if !strings.Contains(rendered, "ghcr.io") {
			t.Errorf("a registry target rendered as %q, want it to still name the registry it is", rendered)
		}
	}
}

func TestAStackPlanCarryingARegistryRendersWithoutItsPassword(t *testing.T) {
	target := providerkit.RegistryTarget{Server: "ghcr.io", Namespace: "acme", Username: "acme-bot", Password: "ghp_livesecret"}
	plan := providerkit.StackPlan{
		Kind: providerkit.StackApp,
		Images: providerkit.ImagePlan{
			Store:  providerkit.RegistryImages(target),
			Pushes: []providerkit.ImagePush{{App: "web", Source: "ocel/web@sha256:abc", Target: target.Coordinate("web", "sha256-abc"), Digest: "sha256:abc"}},
		},
	}
	unrendered := providerkit.StackPlan{
		Kind:   providerkit.StackApp,
		Images: providerkit.ImagePlan{Store: keptSecret{password: "ghp_livesecret"}, Pushes: plan.Images.Pushes},
	}

	for _, rendered := range []string{
		fmt.Sprintf("%v", plan),
		fmt.Sprintf("%+v", plan),
		fmt.Sprintf("%#v", plan),
		fmt.Sprintf("%+v", plan.Images),
		fmt.Sprintf("%#v", plan.Images.Store),
		fmt.Sprintf("%+v", unrendered),
		fmt.Sprintf("%#v", unrendered.Images),
	} {
		if strings.Contains(rendered, "ghp_livesecret") {
			t.Errorf("a stack plan rendered as %q, and the registry password rides along into any log line that prints one", rendered)
		}
		if !strings.Contains(rendered, "ghcr.io") {
			t.Errorf("a stack plan rendered as %q, want it to still name the registry its images are pushed to", rendered)
		}
	}
}

type keptSecret struct{ password string }

func (keptSecret) Has(context.Context, providerkit.ImagePush) (bool, error) { return false, nil }

func (keptSecret) Push(context.Context, providerkit.ImagePush, providerkit.Reporter) error {
	return nil
}
