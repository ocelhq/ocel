package s3

import (
	"context"
	"testing"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

func TestListingFromTheTopOfABucketAnswersItsObjectsRatherThanPagesOfTheStoresBookkeeping(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	sessionsInSequence(h)
	for range 3 {
		h.presign(t, "a.png", 3, "image/png")
	}
	h.store.put("store", "a.png", []byte("abc"), "image/png")

	listed, err := h.svc.List(context.Background(), &bucketv1.ListRequest{Bucket: "store", Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed.GetObjects()) != 1 || listed.GetObjects()[0].GetKey() != "a.png" {
		t.Fatalf("the first page lists %v with cursor %q, want a.png: three open uploads filled it with sessions the app cannot see", listed.GetObjects(), listed.GetNextCursor())
	}
	if listed.GetNextCursor() != "" {
		t.Fatalf("the cursor is %q after the last object", listed.GetNextCursor())
	}
}
