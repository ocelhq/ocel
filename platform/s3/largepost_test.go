//go:build integration

package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

const largeUploadBytes = 32 << 20

func TestAStoreTakesABrowserUploadOfThirtyTwoMebibytesTheServiceSigned(t *testing.T) {
	ctx := context.Background()
	store := aRunningStore(t, "largeuploads")
	svc := New(Config{
		Objects:      store.Client(),
		Internal:     store.Presigner(),
		Granted:      []string{"largeuploads"},
		Sessions:     "largeuploads/" + ReservedKeyPrefix,
		PostPolicies: true,
	})
	signed, err := svc.Sign(ctx, &bucketv1.SignRequest{
		Bucket:      "largeuploads",
		Key:         "images/layer.tar",
		Operation:   bucketv1.SignedOperation_SIGNED_OPERATION_POST_UPLOAD,
		Audience:    bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
		Constraints: &bucketv1.SignConstraints{MaxSize: 2 * largeUploadBytes},
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	target := signed.GetTarget()

	payload := make([]byte, largeUploadBytes)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	for name, value := range target.GetFields() {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	file, err := writer.CreateFormFile("file", "layer.tar")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(ctx, target.GetMethod(), target.GetUrl(), &form)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	sent, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sent.Body.Close()
	if sent.StatusCode/100 != 2 {
		t.Fatalf("the store answered %s to a %d-byte browser upload the service signed, want it taken", sent.Status, largeUploadBytes)
	}
	head, err := store.Client().HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("largeuploads"), Key: aws.String("images/layer.tar")})
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if got := aws.ToInt64(head.ContentLength); got != largeUploadBytes {
		t.Errorf("the store holds %d bytes, want the %d uploaded", got, largeUploadBytes)
	}
}
