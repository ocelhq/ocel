package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/naming"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const testToken = "proxy-session-token"

type recordingBuckets struct {
	bucketv1connect.UnimplementedBucketServiceHandler
	presigned []*bucketv1.PresignFile
}

func (r *recordingBuckets) PresignUpload(_ context.Context, req *bucketv1.PresignUploadRequest) (*bucketv1.PresignUploadResponse, error) {
	r.presigned = append(r.presigned, req.GetFiles()...)
	return &bucketv1.PresignUploadResponse{SessionId: "sess_1"}, nil
}

func (r *recordingBuckets) VerifyUploadSignature(context.Context, *bucketv1.VerifyUploadSignatureRequest) (*bucketv1.VerifyUploadSignatureResponse, error) {
	return &bucketv1.VerifyUploadSignatureResponse{Valid: true}, nil
}

func (r *recordingBuckets) GetUploadStatus(context.Context, *bucketv1.GetUploadStatusRequest) (*bucketv1.GetUploadStatusResponse, error) {
	return &bucketv1.GetUploadStatusResponse{}, nil
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

func serveBuckets(t *testing.T) (bucketv1connect.BucketServiceClient, *recordingBuckets) {
	t.Helper()
	svc := &recordingBuckets{}
	ts := httptest.NewServer(NewMux(testToken, svc))
	t.Cleanup(ts.Close)
	return bucketv1connect.NewBucketServiceClient(http.DefaultClient, ts.URL,
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
		"a\nb",
		".",
		"..",
		"",
	}
	for _, key := range escaping {
		t.Run("a key climbing out of the prefix is refused: "+key, func(t *testing.T) {
			t.Parallel()
			client, svc := serveBuckets(t)

			_, err := client.PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{
				Bucket: "storage",
				Files:  []*bucketv1.PresignFile{{Key: key, Name: "photo.jpg", Size: 1, MimeType: "image/jpeg"}},
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
		"my photo.jpg",
		"uploads/Ünïcødé résumé.pdf",
		"写真/猫.jpg",
	}
	for _, key := range legitimate {
		t.Run("a key inside the prefix is signed: "+key, func(t *testing.T) {
			t.Parallel()
			client, svc := serveBuckets(t)

			if _, err := client.PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{
				Bucket: "storage",
				Files:  []*bucketv1.PresignFile{{Key: key, Name: "photo.jpg", Size: 1, MimeType: "image/jpeg"}},
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

	cases := map[string]*bucketv1.PresignUploadRequest{
		"no bucket": {Files: []*bucketv1.PresignFile{{Key: "a.png"}}},
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

func TestTheProxyAnswersForEveryBindingTypeAnAppReachesThroughIt(t *testing.T) {
	t.Parallel()

	services := map[bindingsv1.BindingType]string{
		bindingsv1.BindingType_BINDING_TYPE_BUCKET: bucketv1connect.BucketServiceName,
	}
	mux := NewMux(testToken, &recordingBuckets{})
	for wire := range bindingsv1.BindingType_name {
		kind := bindingsv1.BindingType(wire)
		if !naming.Proxied(kind) {
			continue
		}
		service, known := services[kind]
		if !known {
			t.Errorf("%v is a type an app reaches through the proxy, and nothing here names the service that answers for it", kind)
			continue
		}
		if _, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, "/"+service+"/", nil)); pattern == "" {
			t.Errorf("%v is a type an app reaches through the proxy, and the proxy mounts no %s: preflight lets the deploy past and the app meets the gap at its first call", kind, service)
		}
	}
}
