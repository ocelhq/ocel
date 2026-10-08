package s3

import (
	"context"
	"testing"

	connect "connectrpc.com/connect"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

func TestAnUploadSessionAnswersOnlyACallerGrantedItsBucket(t *testing.T) {
	t.Parallel()
	owner := newHarness(t, func(cfg *Config) {
		cfg.Sessions = "shared/build"
		cfg.Granted = []string{"mine"}
	})
	other := New(Config{
		Objects:  owner.store,
		Internal: presigner(),
		Sessions: "shared/build",
		Granted:  []string{"theirs"},
	})
	ctx := context.Background()
	opened, err := owner.svc.PresignUpload(ctx, &bucketv1.PresignUploadRequest{
		Bucket:   "mine",
		Metadata: []byte(`{"owner":"mine"}`),
		Files:    []*bucketv1.PresignFile{{Key: "a.png", Name: "a.png", Size: 3, MimeType: "image/png"}},
	})
	if err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}
	id := opened.GetSessionId()

	t.Run("the grantee reads its own session", func(t *testing.T) {
		t.Parallel()
		if _, err := owner.svc.GetUploadStatus(ctx, &bucketv1.GetUploadStatusRequest{SessionId: id}); err != nil {
			t.Fatalf("GetUploadStatus: %v", err)
		}
	})
	t.Run("GetUploadStatus of another grant's session is not found", func(t *testing.T) {
		t.Parallel()
		_, err := other.GetUploadStatus(ctx, &bucketv1.GetUploadStatusRequest{SessionId: id})
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("GetUploadStatus = %v, want not found", err)
		}
	})
	t.Run("CompleteUpload of another grant's session is not found", func(t *testing.T) {
		t.Parallel()
		_, err := other.CompleteUpload(ctx, &bucketv1.CompleteUploadRequest{SessionId: id})
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("CompleteUpload = %v, want not found", err)
		}
	})
	t.Run("VerifyUploadSignature of another grant's session is invalid and returns no metadata", func(t *testing.T) {
		t.Parallel()
		signature, err := signUpload("test-secret", id, signedFile{Key: "a.png", Name: "a.png", Size: 3, MimeType: "image/png"})
		if err != nil {
			t.Fatalf("signUpload: %v", err)
		}
		got, err := other.VerifyUploadSignature(ctx, &bucketv1.VerifyUploadSignatureRequest{
			SessionId: id,
			Signature: signature,
			File:      &bucketv1.CompletedFile{Key: "a.png", Name: "a.png", Size: 3, MimeType: "image/png"},
		})
		if err != nil {
			t.Fatalf("VerifyUploadSignature: %v", err)
		}
		if got.GetValid() || got.GetMetadata() != nil {
			t.Fatalf("VerifyUploadSignature = %+v, want invalid with no metadata", got)
		}
	})
}
