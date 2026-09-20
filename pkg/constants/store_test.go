package constants

import (
	"strings"
	"testing"
)

func TestTheSessionsBucketIsANameNoProjectCouldDeclare(t *testing.T) {
	t.Parallel()

	held := StoreSessionsBucket()
	if !strings.HasPrefix(held, "ocel-") || strings.Contains(held, "--") {
		t.Fatalf("StoreSessionsBucket() = %q, want a reserved name a declared bucket's own could never be", held)
	}
	if len(held) < 3 || len(held) > 63 || strings.ToLower(held) != held {
		t.Fatalf("StoreSessionsBucket() = %q, which is not a bucket name an s3 store accepts", held)
	}
}

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
