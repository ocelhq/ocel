package images

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

const (
	localRepository = "ocel/shop/web"
	localTag        = "sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestALocalRepositoryScopedToItsProjectPushesUnderItsAppAlone(t *testing.T) {
	got := Ref(localRepository, localTag, provider.RegistryTarget{Server: "registry.invalid", Namespace: "ocel"})
	want := "registry.invalid/ocel/web:" + localTag
	if got != want {
		t.Errorf("the push writes %q, want %q: the project scopes the repository on the box that built the image, and a registry has one repository per app", got, want)
	}
}

func TestALocalRepositoryScopedToItsProjectKeepsThatScopeWhereNoRegistryTakesIt(t *testing.T) {
	got := Ref(localRepository, localTag, provider.RegistryTarget{})
	want := localRepository + ":" + localTag
	if got != want {
		t.Errorf("the push writes %q, want %q: an image loaded straight onto a box stays named under the project that built it", got, want)
	}
}
