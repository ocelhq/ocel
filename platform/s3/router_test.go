package s3

import (
	"context"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
)

type ownBackend struct {
	bucketv1connect.UnimplementedBucketServiceHandler
	presigned []string
	completed []string
	headed    []string
}

func (o *ownBackend) PresignUpload(_ context.Context, req *bucketv1.PresignUploadRequest) (*bucketv1.PresignUploadResponse, error) {
	o.presigned = append(o.presigned, req.GetBucket())
	return &bucketv1.PresignUploadResponse{SessionId: "sess_0a1b2c"}, nil
}

func (o *ownBackend) CompleteUpload(_ context.Context, req *bucketv1.CompleteUploadRequest) (*bucketv1.CompleteUploadResponse, error) {
	o.completed = append(o.completed, req.GetSessionId())
	return &bucketv1.CompleteUploadResponse{}, nil
}

func (o *ownBackend) Head(_ context.Context, req *bucketv1.HeadRequest) (*bucketv1.HeadResponse, error) {
	o.headed = append(o.headed, req.GetBucket())
	return &bucketv1.HeadResponse{}, nil
}

func boundBackend(t *testing.T, tag, granted string) *Service {
	t.Helper()
	store := newFakeStore()
	return New(Config{
		Tag:       tag,
		Objects:   store,
		Internal:  presigner(),
		External:  func(context.Context) (PresignAPI, string) { return externalPresigner(), "https://r2.example.com" },
		Callbacks: &recordingPoster{},
		Sessions:  granted,
		Granted:   []string{granted},
	})
}

func presignIn(t *testing.T, svc bucketv1connect.BucketServiceHandler, bucket string) string {
	t.Helper()
	resp, err := svc.PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{
		Bucket: bucket,
		Files:  []*bucketv1.PresignFile{{Key: "a.png", Name: "a.png", Size: 3, MimeType: "image/png"}},
	})
	if err != nil {
		t.Fatalf("PresignUpload(%s): %v", bucket, err)
	}
	return resp.GetSessionId()
}

func TestTheRouterSendsEachBucketToTheBackendThatHoldsIt(t *testing.T) {
	own := &ownBackend{}
	uploads := boundBackend(t, "b0", "acme/uploads")
	avatars := boundBackend(t, "b1", "acme-avatars")
	router := route(own, uploads, avatars)

	if _, err := router.Head(context.Background(), &bucketv1.HeadRequest{Bucket: "ocel-owned", Key: "a.png"}); err != nil {
		t.Fatalf("Head(ocel-owned): %v", err)
	}
	if len(own.headed) != 1 {
		t.Errorf("own backend headed %v, want the bucket no binding names sent to it", own.headed)
	}

	session := presignIn(t, router, "acme/uploads")
	if !strings.HasPrefix(session, "sess_b0_") {
		t.Errorf("session = %q, want it tagged with the backend that opened it", session)
	}
	if len(own.presigned) != 0 {
		t.Errorf("own backend presigned %v, want the bound bucket kept off it", own.presigned)
	}

	if _, err := router.CompleteUpload(context.Background(), &bucketv1.CompleteUploadRequest{SessionId: session}); err != nil {
		t.Fatalf("CompleteUpload(%s): %v", session, err)
	}
	if len(own.completed) != 0 {
		t.Errorf("own backend completed %v, want the tagged session completed where it lives", own.completed)
	}

	owned := presignIn(t, router, "ocel-owned")
	if _, err := router.CompleteUpload(context.Background(), &bucketv1.CompleteUploadRequest{SessionId: owned}); err != nil {
		t.Fatalf("CompleteUpload(%s): %v", owned, err)
	}
	if len(own.completed) != 1 || own.completed[0] != owned {
		t.Errorf("own backend completed %v, want its own untagged session", own.completed)
	}
}

func TestARouterWithNoOwnBackendRefusesABucketNoBindingNames(t *testing.T) {
	router := route(nil, boundBackend(t, "b0", "acme/uploads"))

	_, err := router.Head(context.Background(), &bucketv1.HeadRequest{Bucket: "elsewhere", Key: "a.png"})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Head(elsewhere) = %v, want failed precondition", err)
	}
	for _, want := range []string{`"elsewhere"`, "endpoint"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Head(elsewhere) = %v, want it to name %s: this runtime has no store of its own, so a grant is not what is missing", err, want)
		}
	}
	_, err = router.CompleteUpload(context.Background(), &bucketv1.CompleteUploadRequest{SessionId: "sess_0a1b2c"})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("CompleteUpload(untagged) = %v, want not found", err)
	}
}
