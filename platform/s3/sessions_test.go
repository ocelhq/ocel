package s3

import (
	"context"
	"fmt"
	"testing"
	"time"

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

func sessionsInSequence(h *harness) {
	h.svc.newID = func(bucket string, at time.Time) string {
		id := sessionID(h.svc.cfg.Tag, bucket, at, fmt.Sprint(len(h.opened)+1))
		h.opened = append(h.opened, id)
		return id
	}
	h.svc.sweeping = func(run func()) { run() }
}

func TestASessionLongPastItsExpiryIsDeletedWhenTheNextUploadOpens(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	sessionsInSequence(h)
	ctx := context.Background()

	h.presign(t, "a.png", 3, "image/png")
	h.store.put("store", "a.png", []byte("abc"), "image/png")
	if _, err := h.svc.CompleteUpload(ctx, &bucketv1.CompleteUploadRequest{SessionId: h.opened[0]}); err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	h.now = h.now.Add(5 * time.Hour)
	h.presign(t, "b.png", 3, "image/png")

	if _, kept := h.store.objects["store"][SessionKeyPrefix+h.opened[0]]; kept {
		t.Error("a session settled five hours ago is still in the store, so every upload ever made leaves a record behind")
	}
	if _, opened := h.store.objects["store"][SessionKeyPrefix+h.opened[1]]; !opened {
		t.Error("the session just opened was deleted with the old one")
	}
	if _, present := h.store.objects["store"]["a.png"]; !present {
		t.Error("deleting a settled session took the upload it confirmed with it")
	}
}

func TestASessionStillWithinReachOfItsClientIsKept(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	sessionsInSequence(h)
	ctx := context.Background()

	h.presign(t, "a.png", 3, "image/png")
	h.store.put("store", "a.png", []byte("abc"), "image/png")
	if _, err := h.svc.CompleteUpload(ctx, &bucketv1.CompleteUploadRequest{SessionId: h.opened[0]}); err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	h.now = h.now.Add(time.Hour)
	h.presign(t, "b.png", 3, "image/png")

	resp, err := h.svc.CompleteUpload(ctx, &bucketv1.CompleteUploadRequest{SessionId: h.opened[0]})
	if err != nil {
		t.Fatalf("a client polling the session it finished an hour ago was answered %v", err)
	}
	if resp.GetState() != bucketv1.UploadState_UPLOAD_STATE_SUCCEEDED {
		t.Fatalf("state = %v, want SUCCEEDED", resp.GetState())
	}
}

func TestAnAbandonedSessionIsExpiredBeforeItsRecordIsDeleted(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	sessionsInSequence(h)

	h.presign(t, "a.png", 3, "image/png")
	h.store.put("store", "a.png", []byte("abc"), "image/png")
	h.now = h.now.Add(5 * time.Hour)
	h.presign(t, "b.png", 3, "image/png")

	if _, kept := h.store.objects["store"][SessionKeyPrefix+h.opened[0]]; kept {
		t.Error("a session nobody came back to is still in the store")
	}
	if _, present := h.store.objects["store"]["a.png"]; present {
		t.Error("the upload of a session nobody confirmed outlived the session, and nothing will ever claim it")
	}
	if len(h.poster.posts) != 0 {
		t.Errorf("the app was told about an upload its client never confirmed: %+v", h.poster.posts)
	}
}

func TestASessionOfABucketThisAppWasNotGrantedIsLeftForItsOwner(t *testing.T) {
	t.Parallel()
	owner := newHarness(t, func(cfg *Config) {
		cfg.Sessions = "shared/build"
		cfg.Granted = []string{"mine"}
	})
	sessionsInSequence(owner)
	ctx := context.Background()
	if _, err := owner.svc.PresignUpload(ctx, &bucketv1.PresignUploadRequest{
		Bucket: "mine",
		Files:  []*bucketv1.PresignFile{{Key: "a.png", Name: "a.png", Size: 3, MimeType: "image/png"}},
	}); err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}

	other := New(Config{
		Objects:  owner.store,
		Internal: presigner(),
		External: func(context.Context) (PresignAPI, string) { return externalPresigner(), "https://storage.example.com" },
		Sessions: "shared/build",
		Granted:  []string{"theirs"},
	})
	other.now = func() time.Time { return owner.now.Add(5 * time.Hour) }
	other.newID = func(string, time.Time) string { return "sess_theirs" }
	other.sweeping = func(run func()) { run() }
	if _, err := other.PresignUpload(ctx, &bucketv1.PresignUploadRequest{
		Bucket: "theirs",
		Files:  []*bucketv1.PresignFile{{Key: "b.png", Name: "b.png", Size: 3, MimeType: "image/png"}},
	}); err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}

	if _, kept := owner.store.objects["shared"]["build/"+SessionKeyPrefix+owner.opened[0]]; !kept {
		t.Error("another grant's sweep deleted a session it could not read")
	}
}
