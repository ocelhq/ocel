package images

import (
	"strings"
	"testing"
)

func TestTheObjectStoreIsPinnedByDigest(t *testing.T) {
	t.Parallel()

	image := ObjectStore()
	tag, digest, cut := strings.Cut(image, "@sha256:")
	if !cut || len(digest) != 64 {
		t.Fatalf("ObjectStore() = %q, which a registry can move under whoever pulls it", image)
	}
	if !strings.HasPrefix(tag, "rustfs/rustfs:") {
		t.Fatalf("ObjectStore() = %q, want the store a box runs", image)
	}
	if strings.HasSuffix(tag, ":latest") {
		t.Fatalf("ObjectStore() = %q, and latest is whatever was pushed last", image)
	}
}
