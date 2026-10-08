package bucket

import (
	"context"
	"testing"

	connect "connectrpc.com/connect"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

func TestAnUploadSessionAnswersOnlyACallerGrantedItsBucket(t *testing.T) {
	t.Parallel()
	ddb := newFakeDDB()
	owner := newTestService(ddb, &fakePresigner{})
	other := New(Config{DDB: ddb, Presigner: &fakePresigner{}, Table: "sessions", SessionKeyPrefix: testSessionKeyPrefix,
		Granted: func() []string { return []string{"elsewhere"} }})
	ctx := context.Background()
	const key = "k.png"
	if _, err := owner.PresignUpload(ctx, &bucketv1.PresignUploadRequest{
		Bucket:   "storage",
		Metadata: []byte("meta"),
		Files:    []*bucketv1.PresignFile{{Key: key, Name: key, Size: 10, MimeType: "image/png"}},
	}); err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}

	t.Run("GetUploadStatus of another grant's session is not found", func(t *testing.T) {
		t.Parallel()
		_, err := other.GetUploadStatus(ctx, &bucketv1.GetUploadStatusRequest{SessionId: "sess_fixed"})
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("GetUploadStatus = %v, want not found", err)
		}
	})
	t.Run("CompleteUpload of another grant's session is not found", func(t *testing.T) {
		t.Parallel()
		_, err := other.CompleteUpload(ctx, &bucketv1.CompleteUploadRequest{SessionId: "sess_fixed"})
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("CompleteUpload = %v, want not found", err)
		}
	})
	t.Run("VerifyUploadSignature of another grant's session is invalid and returns no metadata", func(t *testing.T) {
		t.Parallel()
		got, err := other.VerifyUploadSignature(ctx, &bucketv1.VerifyUploadSignatureRequest{
			SessionId: "sess_fixed",
			Signature: mustSign(t, "test-secret", "sess_fixed", SignedFile{Key: key, Name: key, Size: 10, MimeType: "image/png"}),
			File:      &bucketv1.CompletedFile{Key: key, Name: key, Size: 10, MimeType: "image/png"},
		})
		if err != nil {
			t.Fatalf("VerifyUploadSignature: %v", err)
		}
		if got.GetValid() || got.GetMetadata() != nil {
			t.Fatalf("VerifyUploadSignature = %+v, want invalid with no metadata", got)
		}
	})
}
