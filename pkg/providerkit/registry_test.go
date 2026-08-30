package providerkit_test

import (
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
