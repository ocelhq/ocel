package s3

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/constants"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/enginetest"
)

func TestMain(m *testing.M) { os.Exit(enginetest.Main(m)) }

func aRunningStore(t *testing.T, bucket string) Store {
	t.Helper()
	running := enginetest.SharedObjectStore(t)
	running.ClaimBucket(t, bucket)
	store := Store{
		Endpoint:        running.Endpoint,
		Region:          running.Region,
		AccessKeyID:     running.AccessKeyID,
		SecretAccessKey: running.SecretKey,
		PathStyle:       true,
	}
	if _, err := store.Client().CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create the bucket to drive: %v", err)
	}
	return store
}

func TestAStoreTakesThePutTheServiceSignedWithMetadataAndCacheControl(t *testing.T) {
	ctx := context.Background()
	store := aRunningStore(t, "uploads")
	svc := New(Config{
		Objects:  store.Client(),
		Internal: store.Presigner(),
		Granted:  []string{"uploads"},
		Sessions: "uploads/" + constants.ReservedKeyPrefix,
	})

	resp, err := svc.Sign(ctx, &bucketv1.SignRequest{
		Bucket:    "uploads",
		Key:       "notes/hello.txt",
		Operation: bucketv1.SignedOperation_SIGNED_OPERATION_PUT,
		Audience:  bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
		Constraints: &bucketv1.SignConstraints{
			ContentType:  "text/plain",
			CacheControl: "public, max-age=31536000",
			Metadata:     map[string]string{"owner": "ada"},
		},
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	target := resp.GetTarget()

	body := []byte("hello")
	req, err := http.NewRequestWithContext(ctx, target.GetMethod(), target.GetUrl(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range target.GetHeaders() {
		req.Header.Set(name, value)
	}
	sent, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sent.Body.Close()
	if sent.StatusCode != http.StatusOK {
		t.Fatalf("the store answered %s to the put the service signed, so the headers it asked for are outside the signature", sent.Status)
	}

	head, err := store.Client().HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String("uploads"),
		Key:    aws.String("notes/hello.txt"),
	})
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if got := head.Metadata["owner"]; got != "ada" {
		t.Errorf("metadata owner = %q, want the value the caller asked to be signed", got)
	}
	if got := aws.ToString(head.CacheControl); got != "public, max-age=31536000" {
		t.Errorf("cache-control = %q, want the value the caller asked to be signed", got)
	}
	if got := aws.ToString(head.ContentType); got != "text/plain" {
		t.Errorf("content-type = %q", got)
	}

	unsigned, err := http.NewRequestWithContext(ctx, target.GetMethod(), target.GetUrl(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range target.GetHeaders() {
		unsigned.Header.Set(name, value)
	}
	unsigned.Header.Set("x-amz-meta-smuggled", "yes")
	refused, err := http.DefaultClient.Do(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	defer refused.Body.Close()
	if refused.StatusCode == http.StatusOK {
		t.Error("the store took a vendor header the signature never covered, so signing metadata would be pointless")
	}
}

func TestMetadataBeyondTheCapIsRefusedBeforeItIsSigned(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	big := make([]byte, 2048)
	for i := range big {
		big[i] = 'a'
	}
	_, err := h.svc.Sign(context.Background(), &bucketv1.SignRequest{
		Bucket:    "store",
		Key:       "big.txt",
		Operation: bucketv1.SignedOperation_SIGNED_OPERATION_PUT,
		Constraints: &bucketv1.SignConstraints{
			Metadata: map[string]string{"blob": string(big)},
		},
	})
	if err == nil {
		t.Fatal("Sign took metadata past the 2 KB a store will hold")
	}

	_, err = h.svc.CreateMultipart(context.Background(), &bucketv1.CreateMultipartRequest{
		Bucket:   "store",
		Key:      "big.txt",
		Metadata: map[string]string{"blob": string(big)},
	})
	if err == nil {
		t.Fatal("CreateMultipart took metadata past the 2 KB a store will hold")
	}
}
