package s3

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"google.golang.org/protobuf/proto"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

func TestABoundBucketIsCheckedAndServedOnARealStore(t *testing.T) {
	ctx := context.Background()
	store := aRunningStore(t, "bound")
	record := &bindingsv1.BucketProperties{
		Bucket:          "bound",
		Endpoint:        store.Endpoint,
		Region:          store.Region,
		AccessKeyId:     store.AccessKeyID,
		SecretAccessKey: store.SecretAccessKey,
		PathStyle:       true,
		Prefix:          "app/",
	}

	if _, err := Check(ctx, record, Want{}); err != nil {
		t.Fatalf("Check = %v, want a bucket that answers passed", err)
	}
	absent := proto.CloneOf(record)
	absent.Bucket = "never-made"
	if _, err := Check(ctx, absent, Want{}); err == nil {
		t.Error("Check = nil, want a bucket the store does not hold refused")
	}

	if _, err := store.Client().PutBucketCors(ctx, &s3.PutBucketCorsInput{
		Bucket: aws.String("bound"),
		CORSConfiguration: &s3types.CORSConfiguration{CORSRules: []s3types.CORSRule{{
			AllowedOrigins: []string{"https://acme.com"}, AllowedMethods: []string{"PUT", "POST", "GET"},
		}}},
	}); err == nil {
		_, err := Check(ctx, record, Want{Origins: []string{"https://acme.com", "https://admin.acme.com"}})
		if err == nil || !strings.Contains(err.Error(), "https://admin.acme.com") {
			t.Errorf("Check = %v, want the origin the store's CORS rules lack named", err)
		}
	}

	poster := &recordingPoster{}
	svc := Bound("b0", "OCEL_RESOURCE_BUCKET_bound", record, poster)
	opened, err := svc.PresignUpload(ctx, &bucketv1.PresignUploadRequest{
		Bucket:          "OCEL_RESOURCE_BUCKET_bound",
		CallbackBaseUrl: "http://127.0.0.1:1/api/upload",
		Files:           []*bucketv1.PresignFile{{Key: "notes/a.txt", Name: "a.txt", Size: 5, MimeType: "text/plain"}},
	})
	if err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}
	target := opened.GetFiles()[0]
	put, err := http.NewRequestWithContext(ctx, target.GetMethod(), target.GetUrl(), bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range target.GetHeaders() {
		put.Header.Set(name, value)
	}
	put.ContentLength = 5
	sent, err := http.DefaultClient.Do(put)
	if err != nil {
		t.Fatal(err)
	}
	_ = sent.Body.Close()
	if sent.StatusCode != http.StatusOK {
		t.Fatalf("the store answered %s to the upload the bound backend signed", sent.Status)
	}
	if _, err := store.Client().HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("bound"), Key: aws.String("app/notes/a.txt")}); err != nil {
		t.Fatalf("the upload is not under the binding's prefix: %v", err)
	}

	done, err := svc.CompleteUpload(ctx, &bucketv1.CompleteUploadRequest{SessionId: opened.GetSessionId()})
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if done.GetState() != bucketv1.UploadState_UPLOAD_STATE_SUCCEEDED {
		t.Errorf("CompleteUpload = %v, want the upload the store holds confirmed", done.GetState())
	}
}
