package bucket

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"google.golang.org/protobuf/types/known/durationpb"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

func realPresigner() *s3.PresignClient {
	return s3.NewPresignClient(s3.New(s3.Options{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", ""),
	}))
}

func newSigningService(objects *fakeS3) *Service {
	return New(Config{
		DDB:              newFakeDDB(),
		Presigner:        realPresigner(),
		Objects:          objects,
		Table:            "sessions",
		SessionKeyPrefix: testSessionKeyPrefix,
		Granted:          func() []string { return []string{"storage", "b"} },
	})
}

func TestSign(t *testing.T) {
	t.Parallel()

	t.Run("a read is signed as a GET the browser can follow", func(t *testing.T) {
		t.Parallel()
		svc := newSigningService(newFakeS3())

		resp, err := svc.Sign(context.Background(), &bucketv1.SignRequest{
			Bucket:    "storage",
			Key:       "avatars/a.png",
			Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
			Audience:  bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL,
			ExpiresIn: durationpb.New(600_000_000_000),
		})
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		target := resp.GetTarget()
		if target.GetMethod() != "GET" {
			t.Fatalf("method = %q, want GET", target.GetMethod())
		}
		if !strings.Contains(target.GetUrl(), "avatars/a.png") || !strings.Contains(target.GetUrl(), "X-Amz-Signature") {
			t.Fatalf("url = %q, want a signed url for the key", target.GetUrl())
		}
		if got := signedExpiry(t, target.GetUrl()); got != "600" {
			t.Fatalf("X-Amz-Expires = %q, want the 600 seconds the caller asked for", got)
		}
	})

	t.Run("a download filename rides on the signature, not on the caller", func(t *testing.T) {
		t.Parallel()
		svc := newSigningService(newFakeS3())

		resp, err := svc.Sign(context.Background(), &bucketv1.SignRequest{
			Bucket:      "storage",
			Key:         "reports/q3.pdf",
			Operation:   bucketv1.SignedOperation_SIGNED_OPERATION_GET,
			Audience:    bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL,
			Constraints: &bucketv1.SignConstraints{DownloadFilename: "Q3 report.pdf"},
		})
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		if !strings.Contains(resp.GetTarget().GetUrl(), "response-content-disposition") {
			t.Fatalf("url = %q, want the response-content-disposition override signed in", resp.GetTarget().GetUrl())
		}
	})

	t.Run("a write is signed as a PUT that states the headers it covers", func(t *testing.T) {
		t.Parallel()
		svc := newSigningService(newFakeS3())

		resp, err := svc.Sign(context.Background(), &bucketv1.SignRequest{
			Bucket:      "storage",
			Key:         "avatars/a.png",
			Operation:   bucketv1.SignedOperation_SIGNED_OPERATION_PUT,
			Audience:    bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL,
			Constraints: &bucketv1.SignConstraints{ContentType: "image/png", IfNoneMatch: "*"},
		})
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		target := resp.GetTarget()
		if target.GetMethod() != "PUT" {
			t.Fatalf("method = %q, want PUT", target.GetMethod())
		}
		if len(target.GetFields()) != 0 {
			t.Fatalf("fields = %v, want none on a PUT target", target.GetFields())
		}
	})

	t.Run("a browser upload is signed as a POST policy bounding the size", func(t *testing.T) {
		t.Parallel()
		svc := newSigningService(newFakeS3())

		resp, err := svc.Sign(context.Background(), &bucketv1.SignRequest{
			Bucket:      "storage",
			Key:         "avatars/a.png",
			Operation:   bucketv1.SignedOperation_SIGNED_OPERATION_POST_UPLOAD,
			Audience:    bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL,
			Constraints: &bucketv1.SignConstraints{ContentType: "image/png", MaxSize: 5 << 20},
		})
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		target := resp.GetTarget()
		if target.GetMethod() != "POST" {
			t.Fatalf("method = %q, want POST", target.GetMethod())
		}
		if target.GetFields()["policy"] == "" || target.GetFields()["key"] != "avatars/a.png" {
			t.Fatalf("fields = %v, want the policy and key a form post carries", target.GetFields())
		}
	})
}

func TestMultipart(t *testing.T) {
	t.Parallel()

	t.Run("an upload is created, its parts signed, and the object assembled", func(t *testing.T) {
		t.Parallel()
		objects := newFakeS3()
		svc := newSigningService(objects)
		ctx := context.Background()

		created, err := svc.CreateMultipart(ctx, &bucketv1.CreateMultipartRequest{
			Bucket:      "storage",
			Key:         "big.bin",
			ContentType: "application/octet-stream",
		})
		if err != nil {
			t.Fatalf("CreateMultipart: %v", err)
		}
		if created.GetUploadId() == "" {
			t.Fatal("CreateMultipart returned no upload id")
		}

		signed, err := svc.SignParts(ctx, &bucketv1.SignPartsRequest{
			Bucket:      "storage",
			Key:         "big.bin",
			UploadId:    created.GetUploadId(),
			PartNumbers: []int32{1, 2},
			Audience:    bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
		})
		if err != nil {
			t.Fatalf("SignParts: %v", err)
		}
		if len(signed.GetParts()) != 2 {
			t.Fatalf("SignParts returned %d urls, want one per part", len(signed.GetParts()))
		}
		for i, part := range signed.GetParts() {
			if part.GetPartNumber() != int32(i+1) || !strings.Contains(part.GetUrl(), "partNumber=") {
				t.Fatalf("part %d = %+v, want a signed upload-part url", i, part)
			}
		}

		done, err := svc.CompleteMultipart(ctx, &bucketv1.CompleteMultipartRequest{
			Bucket:   "storage",
			Key:      "big.bin",
			UploadId: created.GetUploadId(),
			Parts: []*bucketv1.CompletedPart{
				{PartNumber: 1, Etag: `"p1"`},
				{PartNumber: 2, Etag: `"p2"`},
			},
		})
		if err != nil {
			t.Fatalf("CompleteMultipart: %v", err)
		}
		if done.GetObject().GetKey() != "big.bin" {
			t.Fatalf("CompleteMultipart = %+v, want the assembled object", done.GetObject())
		}
	})

	t.Run("an abandoned upload is aborted so its parts stop costing storage", func(t *testing.T) {
		t.Parallel()
		objects := newFakeS3()
		svc := newSigningService(objects)
		ctx := context.Background()

		created, err := svc.CreateMultipart(ctx, &bucketv1.CreateMultipartRequest{Bucket: "storage", Key: "big.bin"})
		if err != nil {
			t.Fatalf("CreateMultipart: %v", err)
		}
		if _, err := svc.AbortMultipart(ctx, &bucketv1.AbortMultipartRequest{
			Bucket:   "storage",
			Key:      "big.bin",
			UploadId: created.GetUploadId(),
		}); err != nil {
			t.Fatalf("AbortMultipart: %v", err)
		}
		if len(objects.uploads) != 0 {
			t.Fatalf("uploads still open after the abort: %v", objects.uploads)
		}
	})
}

func TestCompleteUpload(t *testing.T) {
	t.Parallel()

	svc := newTestService(newFakeDDB(), &fakePresigner{})
	if _, err := svc.PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{
		Bucket: "storage",
		Files:  []*bucketv1.PresignFile{{Key: "k", Name: "k", Size: 1, MimeType: "text/plain"}},
	}); err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}

	t.Run("a client confirming an upload the store has not notified is still pending", func(t *testing.T) {
		resp, err := svc.CompleteUpload(context.Background(), &bucketv1.CompleteUploadRequest{SessionId: "sess_fixed"})
		if err != nil {
			t.Fatalf("CompleteUpload: %v", err)
		}
		if resp.GetState() != bucketv1.UploadState_UPLOAD_STATE_PENDING {
			t.Fatalf("state = %v, want PENDING until the store's notification lands", resp.GetState())
		}
	})
}

func signedExpiry(t *testing.T, signed string) string {
	t.Helper()
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("X-Amz-Expires")
}
