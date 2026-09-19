package constants

import (
	"strings"
	"testing"
)

func TestTheObjectStoreIsPinnedByDigest(t *testing.T) {
	t.Parallel()

	image := ObjectStoreImage()

	tag, digest, cut := strings.Cut(image, "@sha256:")
	if !cut || len(digest) != 64 {
		t.Fatalf("ObjectStoreImage() = %q, which a registry can move under whoever pulls it", image)
	}
	if !strings.HasPrefix(tag, "rustfs/rustfs:") {
		t.Fatalf("ObjectStoreImage() = %q, want the store a box runs", image)
	}
	if strings.HasSuffix(tag, ":latest") {
		t.Fatalf("ObjectStoreImage() = %q, and latest is whatever was pushed last", image)
	}
}
