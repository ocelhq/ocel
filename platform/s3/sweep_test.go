package s3

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

type countingStore struct {
	*fakeStore

	mu      sync.Mutex
	listed  int
	refuses bool
}

func (c *countingStore) ListMultipartUploads(_ context.Context, _ *s3.ListMultipartUploadsInput, _ ...func(*s3.Options)) (*s3.ListMultipartUploadsOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listed++
	if c.refuses {
		return nil, errors.New("this store lists no uploads")
	}
	return &s3.ListMultipartUploadsOutput{}, nil
}

func TestABucketIsSweptAtMostOnceAnHourHoweverOftenItIsWrittenTo(t *testing.T) {
	t.Parallel()

	counted := &countingStore{fakeStore: newFakeStore()}
	service := New(Config{Objects: counted, SweepUploads: true, Granted: []string{"counted/app"}})
	clock := time.Now().UTC()
	service.now = func() time.Time { return clock }
	service.sweeping = func(run func()) { run() }

	for range 3 {
		if _, err := service.CreateMultipart(context.Background(), &bucketv1.CreateMultipartRequest{
			Bucket: "counted/app", Key: "one.bin",
		}); err != nil {
			t.Fatalf("CreateMultipart() = %v", err)
		}
	}
	if counted.listed != 1 {
		t.Errorf("three uploads swept the bucket %d times, and a sweep on every request is a listing charged to every write", counted.listed)
	}

	clock = clock.Add(61 * time.Minute)
	if _, err := service.CreateMultipart(context.Background(), &bucketv1.CreateMultipartRequest{
		Bucket: "counted/app", Key: "two.bin",
	}); err != nil {
		t.Fatalf("CreateMultipart() = %v", err)
	}
	if counted.listed != 2 {
		t.Errorf("the bucket was swept %d times in two hours, want one sweep an hour", counted.listed)
	}
}

func TestAStoreThatExpiresItsOwnUploadsIsNeverSwept(t *testing.T) {
	t.Parallel()

	counted := &countingStore{fakeStore: newFakeStore()}
	service := New(Config{Objects: counted, Granted: []string{"counted/app"}})
	service.sweeping = func(run func()) { run() }

	if _, err := service.CreateMultipart(context.Background(), &bucketv1.CreateMultipartRequest{
		Bucket: "counted/app", Key: "one.bin",
	}); err != nil {
		t.Fatalf("CreateMultipart() = %v", err)
	}
	if counted.listed != 0 {
		t.Errorf("a store that abandons its own unfinished uploads was listed %d times anyway", counted.listed)
	}
}

func TestASweepTheStoreRefusesLeavesTheUploadItWasOpeningInPlace(t *testing.T) {
	t.Parallel()

	counted := &countingStore{fakeStore: newFakeStore(), refuses: true}
	service := New(Config{Objects: counted, SweepUploads: true, Granted: []string{"counted/app"}})
	service.sweeping = func(run func()) { run() }

	resp, err := service.CreateMultipart(context.Background(), &bucketv1.CreateMultipartRequest{
		Bucket: "counted/app", Key: "one.bin",
	})
	if err != nil {
		t.Fatalf("CreateMultipart() = %v, and a caller's upload is not the sweep's to refuse", err)
	}
	if resp.GetUploadId() == "" {
		t.Fatal("the caller was handed no upload")
	}
}

type abandonedUploads struct {
	*fakeStore

	mu      sync.Mutex
	refused string
	aborted []string
}

func (a *abandonedUploads) ListMultipartUploads(_ context.Context, _ *s3.ListMultipartUploadsInput, _ ...func(*s3.Options)) (*s3.ListMultipartUploadsOutput, error) {
	started := aws.Time(time.Now().Add(-2 * orphanAge))
	return &s3.ListMultipartUploadsOutput{Uploads: []s3types.MultipartUpload{
		{Key: aws.String("app/first.bin"), UploadId: aws.String("first"), Initiated: started},
		{Key: aws.String("app/second.bin"), UploadId: aws.String("second"), Initiated: started},
	}}, nil
}

func (a *abandonedUploads) AbortMultipartUpload(_ context.Context, in *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if aws.ToString(in.Key) == a.refused {
		return nil, errors.New("the store answered 500 Internal Error")
	}
	a.aborted = append(a.aborted, aws.ToString(in.Key))
	return &s3.AbortMultipartUploadOutput{}, nil
}

func TestOneAbandonedUploadTheStoreRefusesToAbortLeavesTheSweepGoing(t *testing.T) {
	t.Parallel()

	store := &abandonedUploads{fakeStore: newFakeStore(), refused: "app/first.bin"}
	service := New(Config{Objects: store, SweepUploads: true, Granted: []string{"counted/app"}})
	service.sweepUploads(context.Background(), scopeOf("counted/app"))

	if len(store.aborted) != 1 || store.aborted[0] != "app/second.bin" {
		t.Errorf("aborted %v, want the second upload aborted after the store refused the first", store.aborted)
	}
}
