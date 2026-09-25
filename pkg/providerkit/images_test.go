package providerkit

import "testing"

const (
	localRepository = "ocel/shop/web"
	localTag        = "sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestALocalRepositoryScopedToItsProjectPushesUnderItsAppAlone(t *testing.T) {
	got := coordinate(localRepository, localTag, RegistryTarget{Server: "registry.invalid", Namespace: "ocel"})
	want := "registry.invalid/ocel/web:" + localTag
	if got != want {
		t.Errorf("the push writes %q, want %q: the project scopes the repository on the box that built the image, and a registry holds one repository per app", got, want)
	}
}

func TestALocalRepositoryScopedToItsProjectKeepsThatScopeWhereNoRegistryTakesIt(t *testing.T) {
	got := coordinate(localRepository, localTag, RegistryTarget{})
	want := localRepository + ":" + localTag
	if got != want {
		t.Errorf("the push writes %q, want %q: an image loaded straight onto a box stays named under the project that built it", got, want)
	}
}
