package providerkit

import "testing"

const localRef = "ocel/shop/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestALocalRepositoryScopedToItsProjectPushesUnderItsAppAlone(t *testing.T) {
	push, err := imagePush("web", localRef, RegistryTarget{Server: "registry.invalid", Namespace: "ocel"})
	if err != nil {
		t.Fatalf("imagePush() = %v", err)
	}
	if push.Source != localRef {
		t.Errorf("the push reads %q from the local store, want %q", push.Source, localRef)
	}
	want := "registry.invalid/ocel/web:sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if push.Target != want {
		t.Errorf("the push writes %q, want %q: the project scopes the repository on the box that built the image, and a registry holds one repository per app", push.Target, want)
	}
}

func TestALocalRepositoryScopedToItsProjectKeepsThatScopeWhereNoRegistryTakesIt(t *testing.T) {
	push, err := imagePush("web", localRef, RegistryTarget{})
	if err != nil {
		t.Fatalf("imagePush() = %v", err)
	}
	want := "ocel/shop/web:sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if push.Target != want {
		t.Errorf("the push writes %q, want %q: an image loaded straight onto a box stays named under the project that built it", push.Target, want)
	}
}
