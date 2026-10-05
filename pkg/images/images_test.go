package images

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

const localTag = "sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var registry = provider.RegistryTarget{Server: "registry.invalid", Namespace: "ocel"}

func TestAnImagePushedToARegistryIsNamedByItsProjectAndApp(t *testing.T) {
	for repository, want := range map[string]string{
		"ocel/shop/web":  "registry.invalid/ocel/shop.web:" + localTag,
		"web-api-orders": "registry.invalid/ocel/shop.web-api-orders:" + localTag,
	} {
		if got := FormatRef("shop", repository, localTag, registry); got != want {
			t.Errorf("FormatRef(shop, %s) = %q, want %q", repository, got, want)
		}
	}
}

func TestTheSameAppInTwoProjectsIsPushedToTwoRepositories(t *testing.T) {
	shop := FormatRef("shop", "web", localTag, registry)
	blog := FormatRef("blog", "web", localTag, registry)
	if shop == blog {
		t.Errorf("projects shop and blog both push web to %q: retention of one would remove what the other still runs", shop)
	}
}

func TestAProjectAndAppThatJoinToTheSameWordsStayApart(t *testing.T) {
	if a, b := FormatRef("a-b", "c", localTag, registry), FormatRef("a", "b-c", localTag, registry); a == b {
		t.Errorf("project a-b's app c and project a's app b-c both push to %q", a)
	}
}

func TestAProjectSlugIsSanitizedIntoTheRepositoryName(t *testing.T) {
	want := "registry.invalid/ocel/my-shop.web:" + localTag
	if got := FormatRef("My Shop", "web", localTag, registry); got != want {
		t.Errorf("FormatRef(My Shop, web) = %q, want %q", got, want)
	}
}

func TestTheLocalRepositoryOfAnImageIsItsProjectAndAppUnderTheOcelNamespace(t *testing.T) {
	if got, want := LocalRepository("My Shop", "web"), "ocel/my-shop/web"; got != want {
		t.Errorf("LocalRepository(My Shop, web) = %q, want %q", got, want)
	}
}

func TestAnImageLoadedStraightOntoABoxIsNamedUnderItsProject(t *testing.T) {
	for repository, want := range map[string]string{
		"ocel/shop/web":  "ocel/shop/web:" + localTag,
		"web-api-orders": "ocel/shop/web-api-orders:" + localTag,
	} {
		if got := FormatRef("shop", repository, localTag, provider.RegistryTarget{}); got != want {
			t.Errorf("FormatRef(shop, %s) with no registry = %q, want %q", repository, got, want)
		}
	}
}
