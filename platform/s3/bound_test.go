package s3

import (
	"context"
	"net/url"
	"strings"
	"testing"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

func boundRecord() *bindingsv1.BucketProperties {
	return &bindingsv1.BucketProperties{
		Bucket:          "acme",
		Endpoint:        "https://abc.r2.cloudflarestorage.com",
		Region:          "auto",
		Prefix:          "uploads/",
		AccessKeyId:     "AKID",
		SecretAccessKey: "secret",
	}
}

func TestABoundBucketIsServedUnderItsBindingsKeyAndThePrefixItNames(t *testing.T) {
	store := newFakeStore()
	svc := bound("b0", "OCEL_RESOURCE_BUCKET_uploads", boundRecord(), &recordingPoster{}, store)

	if !svc.hasBucket("OCEL_RESOURCE_BUCKET_uploads") {
		t.Fatal("the backend stores nothing under the binding's key, the name the app reads off the record")
	}
	store.put("acme", "uploads/a.png", []byte("abc"), "image/png")
	store.put("acme", "elsewhere/b.png", []byte("abc"), "image/png")

	listed, err := svc.List(context.Background(), &bucketv1.ListRequest{Bucket: "OCEL_RESOURCE_BUCKET_uploads"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed.GetObjects()) != 1 || listed.GetObjects()[0].GetKey() != "a.png" {
		t.Fatalf("List = %+v, want the one object under the prefix, the prefix hidden", listed.GetObjects())
	}

	resp, err := svc.PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{
		Bucket: "OCEL_RESOURCE_BUCKET_uploads",
		Files:  []*bucketv1.PresignFile{{Key: "c.png", Name: "c.png", Size: 3, MimeType: "image/png"}},
	})
	if err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}
	if !strings.HasPrefix(resp.GetSessionId(), "sess_b0_") {
		t.Errorf("session = %q, want it tagged", resp.GetSessionId())
	}
	if _, kept := store.objects["acme"]["uploads/"+sessionPrefix+resp.GetSessionId()]; !kept {
		t.Errorf("store = %v, want the session kept under the binding's prefix", store.objects["acme"])
	}
	target, err := url.Parse(resp.GetFiles()[0].GetUrl())
	if err != nil || !strings.HasSuffix(target.Host, "abc.r2.cloudflarestorage.com") || target.Path != "/uploads/c.png" {
		t.Errorf("upload target = %v, want it signed for the store's own endpoint, under the prefix", target)
	}
}
