package imagebuild

import (
	"strings"
	"testing"
)

const someDigest = "sha256:30b585f5c19dd011bedba3bd1ca35d5b53d9db693b3f36295a09fa0a8d77c239"

func TestTheImageIsNamedAfterTheAppAndAddressedByItsDigest(t *testing.T) {
	image, err := imageFor("Web API", someDigest)
	if err != nil {
		t.Fatalf("imageFor() = %v", err)
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

func TestAnImageWithNoDigestIsRefusedRatherThanNamedAfterNothing(t *testing.T) {
	for _, digest := range []string{"", "latest", "sha256:short", "md5:30b585f5c19dd011bedba3bd1ca35d5b53d9db693b3f36295a09fa0a8d77c239"} {
		if _, err := imageFor("web", digest); err == nil {
			t.Errorf("imageFor(%q) succeeded, so a release could be pinned to something that is not a digest", digest)
		}
	}
}
