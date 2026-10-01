package images

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/kvstore"
)

func TestEveryValkeyVersionAStoreRunsIsPinnedByDigest(t *testing.T) {
	t.Parallel()

	if got, want := ValkeyVersions(), []string{"8", "9"}; !slices.Equal(got, want) {
		t.Fatalf("ValkeyVersions() = %v, want %v", got, want)
	}
	for _, version := range ValkeyVersions() {
		if err := kvstore.RefuseVersion(version); err != nil {
			t.Errorf("Valkey(%q) is pinned, and the build refuses a store declaring it: %v", version, err)
		}
		image, pinned := Valkey(version)
		if !pinned {
			t.Fatalf("Valkey(%q) names no image, and ValkeyVersions() lists it", version)
		}
		tag, digest, cut := strings.Cut(image, "@sha256:")
		if !cut || len(digest) != 64 {
			t.Errorf("Valkey(%q) = %q, which a registry can move under whoever pulls it", version, image)
		}
		if !strings.HasPrefix(tag, "valkey/valkey:"+version+".") {
			t.Errorf("Valkey(%q) = %q, which is another major's image", version, image)
		}
	}
}

func TestAnEmptyValkeyVersionRunsTheDefaultMajor(t *testing.T) {
	t.Parallel()

	image, pinned := Valkey("")
	if !pinned || !strings.HasPrefix(image, "valkey/valkey:"+kvstore.DefaultVersion+".") {
		t.Errorf("Valkey(\"\") = %q (pinned %v), want the default major %s", image, pinned, kvstore.DefaultVersion)
	}
}

func TestAValkeyVersionNothingPinsNamesNoImage(t *testing.T) {
	t.Parallel()

	if image, pinned := Valkey("7"); pinned {
		t.Errorf("Valkey(%q) = %q, and nothing pins that version", "7", image)
	}
}
