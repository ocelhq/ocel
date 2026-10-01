package images

import (
	"strings"
	"testing"
)

func TestTheQueueDatabaseIsPinnedByDigestToAPgmqThatReadsOneHeadPerGroup(t *testing.T) {
	t.Parallel()

	image := QueueDatabase()
	tag, digest, cut := strings.Cut(image, "@sha256:")
	if !cut || len(digest) != 64 {
		t.Fatalf("QueueDatabase() = %q, which a registry can move under whoever pulls it", image)
	}
	repository, version, _ := strings.Cut(tag, ":v")
	if !strings.HasPrefix(repository, "ghcr.io/pgmq/pg") || !strings.HasSuffix(repository, "-pgmq") {
		t.Fatalf("QueueDatabase() = %q, want the image pgmq publishes with the extension installed", image)
	}
	if version != "1.13.0" {
		t.Errorf("QueueDatabase() runs pgmq %q, want 1.13.0, which has read_grouped_head (1.11.1 or later)", version)
	}
}
