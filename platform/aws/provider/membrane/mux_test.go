package membrane

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	bucketsv1 "github.com/ocelhq/ocel/pkg/proto/buckets/v1"
	"github.com/ocelhq/ocel/pkg/proto/buckets/v1/bucketsv1connect"
)

const testToken = "membrane-session-token"

type recordingBuckets struct {
	presigned []*bucketsv1.PresignFile
}

func (r *recordingBuckets) PresignUpload(_ context.Context, req *bucketsv1.PresignUploadRequest) (*bucketsv1.PresignUploadResponse, error) {
	r.presigned = append(r.presigned, req.GetFiles()...)
	return &bucketsv1.PresignUploadResponse{SessionId: "sess_1"}, nil
}

func (r *recordingBuckets) VerifyUploadSignature(context.Context, *bucketsv1.VerifyUploadSignatureRequest) (*bucketsv1.VerifyUploadSignatureResponse, error) {
	return &bucketsv1.VerifyUploadSignatureResponse{Valid: true}, nil
}

func (r *recordingBuckets) GetUploadStatus(context.Context, *bucketsv1.GetUploadStatusRequest) (*bucketsv1.GetUploadStatusResponse, error) {
	return &bucketsv1.GetUploadStatusResponse{}, nil
}

type bearer struct{ token string }

func (b bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", channel.FormatAuthHeader(b.token))
		return next(ctx, req)
	}
}

func (b bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (b bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func serveBuckets(t *testing.T) (bucketsv1connect.BucketServiceClient, *recordingBuckets) {
	t.Helper()
	svc := &recordingBuckets{}
	ts := httptest.NewServer(NewMux(testToken, svc))
	t.Cleanup(ts.Close)
	return bucketsv1connect.NewBucketServiceClient(http.DefaultClient, ts.URL,
		connect.WithInterceptors(bearer{token: testToken})), svc
}

func TestPresignUploadKeys(t *testing.T) {
	t.Parallel()

	escaping := []string{
		"../secrets/key",
		"avatars/../../etc/passwd",
		"/etc/passwd",
		"a/./b",
		"a//b",
		`a\b`,
		".",
		"..",
		"",
	}
	for _, key := range escaping {
		t.Run("a key climbing out of the prefix is refused: "+key, func(t *testing.T) {
			t.Parallel()
			client, svc := serveBuckets(t)

			_, err := client.PresignUpload(context.Background(), &bucketsv1.PresignUploadRequest{
				Bucket: "storage",
				Files:  []*bucketsv1.PresignFile{{Key: key, Name: "photo.jpg", Size: 1, MimeType: "image/jpeg"}},
			})

			var connectErr *connect.Error
			if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
				t.Fatalf("PresignUpload(%q) err = %v, want %v", key, err, connect.CodeInvalidArgument)
			}
			if len(svc.presigned) != 0 {
				t.Fatalf("PresignUpload(%q) reached the signer with %+v", key, svc.presigned)
			}
		})
	}

	legitimate := []string{
		"photo.jpg",
		"avatars/photo.jpg",
		"avatars/photo-ab12cd34.jpg",
		"u/42/photo.jpg",
		"readme-ab12cd34",
		"x/c-.png",
	}
	for _, key := range legitimate {
		t.Run("a key inside the prefix is signed: "+key, func(t *testing.T) {
			t.Parallel()
			client, svc := serveBuckets(t)

			if _, err := client.PresignUpload(context.Background(), &bucketsv1.PresignUploadRequest{
				Bucket: "storage",
				Files:  []*bucketsv1.PresignFile{{Key: key, Name: "photo.jpg", Size: 1, MimeType: "image/jpeg"}},
			}); err != nil {
				t.Fatalf("PresignUpload(%q) = %v, want it signed", key, err)
			}
			if len(svc.presigned) != 1 || svc.presigned[0].GetKey() != key {
				t.Fatalf("signer saw %+v, want %q", svc.presigned, key)
			}
		})
	}
}

func TestPresignUploadRequiresABucketAndAFile(t *testing.T) {
	t.Parallel()

	cases := map[string]*bucketsv1.PresignUploadRequest{
		"no bucket": {Files: []*bucketsv1.PresignFile{{Key: "a.png"}}},
		"no files":  {Bucket: "storage"},
	}
	for name, req := range cases {
		t.Run("a request naming "+name+" is refused", func(t *testing.T) {
			t.Parallel()
			client, _ := serveBuckets(t)

			_, err := client.PresignUpload(context.Background(), req)

			var connectErr *connect.Error
			if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
				t.Fatalf("PresignUpload err = %v, want %v", err, connect.CodeInvalidArgument)
			}
		})
	}
}
