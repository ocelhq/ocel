package bucket

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	blobv1 "github.com/ocelhq/ocel/pkg/proto/app/blob/v1"
)

func TestAPresignedTargetStatesEveryVendorHeaderItsSignatureCovers(t *testing.T) {
	t.Parallel()

	presigner := s3.NewPresignClient(s3.New(s3.Options{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", ""),
	}))
	svc := newTestService(newFakeDDB(), presigner)

	resp, err := svc.PresignUpload(context.Background(), &blobv1.PresignUploadRequest{
		Bucket:          "storage",
		CallbackBaseUrl: "https://app.example/api/blob",
		Files:           []*blobv1.PresignFile{{Key: "avatar.png", Name: "avatar.png", Size: 1024, MimeType: "image/png"}},
	})
	if err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}
	target := resp.GetFiles()[0]

	signed, err := url.Parse(target.GetUrl())
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range strings.Split(signed.Query().Get("X-Amz-SignedHeaders"), ";") {
		if !strings.HasPrefix(header, "x-amz-") {
			continue
		}
		if target.GetHeaders()[header] == "" {
			t.Errorf("the signature covers %s and the target states no value for it, so an uploader cannot send it and s3 refuses the PUT", header)
		}
	}
	if got := target.GetHeaders()["x-amz-tagging"]; got != "sessionId=sess_fixed" {
		t.Errorf("x-amz-tagging = %q, want the tag the upload completer finds the session by", got)
	}
}
