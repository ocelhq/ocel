package imagebuild

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

const someDigest = "sha256:30b585f5c19dd011bedba3bd1ca35d5b53d9db693b3f36295a09fa0a8d77c239"

func TestTheImageIsNamedAfterTheAppAndAddressedByItsDigest(t *testing.T) {
	image, err := imageFor("Web API", someDigest)
	if err != nil {
		t.Fatalf("imageFor() = %v", err)
	}

	if image.Name != "web-api" {
		t.Errorf("the image's repository is %q, want the app's own name, which is what a registry is told to hold", image.Name)
	}
	if image.Repository != "ocel/web-api" {
		t.Errorf("the image's repository is %q, want one derived from the app's own name and marked as ocel's", image.Repository)
	}
	if image.Tag != "sha256-"+strings.TrimPrefix(someDigest, "sha256:") {
		t.Errorf("the image's tag is %q, want the digest in the form a docker tag may take", image.Tag)
	}
	if want := "ocel/web-api@" + someDigest; image.Ref != want {
		t.Errorf("the image's ref is %q, want %q, the one coordinate a release is pinned to", image.Ref, want)
	}
}

func TestAnAppNameNoRepositoryCanBeDerivedFromIsRefusedAtTheCoordinate(t *testing.T) {
	if _, err := imageFor(strings.Repeat("a", maxRepository), someDigest); err == nil {
		t.Error("imageFor() named a repository longer than docker holds, so the build fails at the daemon rather than at the coordinate")
	}
}

func TestAnAppNamedInSymbolsAloneIsStillGivenARepository(t *testing.T) {
	for _, app := range []string{"", "***", "-", "9lives"} {
		image, err := imageFor(app, someDigest)
		if err != nil {
			t.Errorf("imageFor(%q) = %v, want a repository derived from what the name leaves", app, err)
			continue
		}
		if !naming.IsRepositorySegment(image.Name) {
			t.Errorf("imageFor(%q) named %q, which is no repository docker can hold", app, image.Repository)
		}
	}
}

func TestAnImageWithNoDigestIsRefusedRatherThanNamedAfterNothing(t *testing.T) {
	for _, digest := range []string{"", "latest", "sha256:short", "md5:30b585f5c19dd011bedba3bd1ca35d5b53d9db693b3f36295a09fa0a8d77c239"} {
		if _, err := imageFor("web", digest); err == nil {
			t.Errorf("imageFor(%q) succeeded, so a release could be pinned to something that is not a digest", digest)
		}
	}
}
