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

func TestACopyAnotherWriteReplacesAtOnceAnswersTheCopysOwnEtag(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.store.put("store", "a.png", []byte("abc"), "image/png")
	h.store.overwrittenAfterWrite = true

	copied, err := h.svc.Copy(context.Background(), &bucketv1.CopyRequest{Bucket: "store", SourceKey: "a.png", DestinationKey: "b.png"})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got := copied.GetObject(); got.GetKey() != "b.png" || got.GetEtag() != `"v2b.png"` {
		t.Fatalf("Copy = %+v, want b.png with the etag the copy wrote, not the later writer's", got)
	}
}

func TestAnAssembledObjectAnotherWriteReplacesAtOnceAnswersTheAssemblysOwnEtag(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.store.overwrittenAfterWrite = true

	done, err := h.svc.CompleteMultipart(context.Background(), &bucketv1.CompleteMultipartRequest{
		Bucket: "store", Key: "big.bin", UploadId: "upload-1",
		Parts: []*bucketv1.CompletedPart{{PartNumber: 1, Etag: `"p1"`}},
	})
	if err != nil {
		t.Fatalf("CompleteMultipart: %v", err)
	}
	if done.GetEtag() != `"v1big.bin"` {
		t.Fatalf("CompleteMultipart etag = %q, want the assembly's own, not the later writer's", done.GetEtag())
	}
}

func TestASignedReadOrWriteNamesTheHeaderTheStoreAnswersItsEtagIn(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(cfg *Config) { cfg.EtagHeader = "x-goog-generation" })

	for _, operation := range []bucketv1.SignedOperation{bucketv1.SignedOperation_SIGNED_OPERATION_GET, bucketv1.SignedOperation_SIGNED_OPERATION_PUT} {
		signed, err := h.svc.Sign(context.Background(), &bucketv1.SignRequest{Bucket: "store", Key: "a.png", Operation: operation, Audience: bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL})
		if err != nil {
			t.Fatalf("Sign %s: %v", operation, err)
		}
		if got := signed.GetTarget().GetEtagHeader(); got != "x-goog-generation" {
			t.Errorf("Sign %s etag header = %q, want the one the store answers with", operation, got)
		}
	}
}
