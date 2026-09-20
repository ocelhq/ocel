package bucket

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

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

func openUpload(t *testing.T, store Store, bucket, key string) string {
	t.Helper()
	out, err := store.Client().CreateMultipartUpload(context.Background(), &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	if err != nil {
		t.Fatalf("open a multipart upload: %v", err)
	}
	return aws.ToString(out.UploadId)
}

func uploadsOpen(t *testing.T, store Store, bucket string) []string {
	t.Helper()
	out, err := store.Client().ListMultipartUploads(context.Background(), &s3.ListMultipartUploadsInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Fatalf("read the uploads the store holds open: %v", err)
	}
	keys := make([]string, 0, len(out.Uploads))
	for _, held := range out.Uploads {
		keys = append(keys, aws.ToString(held.Key))
	}
	return keys
}

func sweeping(t *testing.T, store Store, bucket string, now func() time.Time) *Service {
	t.Helper()
	service := New(Config{
		Objects:      store.Client(),
		Internal:     store.Presigner(),
		SweepUploads: true,
		Granted:      []string{bucket + "/app"},
	})
	service.now = now
	service.sweeping = func(run func()) { run() }
	return service
}

func TestAnUploadLeftOpenForADayIsSweptUpAndOneStartedNowIsLeftAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("stands a real store up")
	}
	store := aRunningStore(t, "swept-bucket")
	openUpload(t, store, "swept-bucket", "app/stale.bin")
	abandoned := time.Now().UTC()
	time.Sleep(2500 * time.Millisecond)

	clock := abandoned.Add(orphanAge + 1500*time.Millisecond)
	service := sweeping(t, store, "swept-bucket", func() time.Time { return clock })

	resp, err := service.CreateMultipart(context.Background(), &bucketv1.CreateMultipartRequest{
		Bucket: "swept-bucket/app", Key: "fresh.bin",
	})
	if err != nil {
		t.Fatalf("CreateMultipart() = %v", err)
	}
	if resp.GetUploadId() == "" {
		t.Fatal("the store opened no upload")
	}

	held := uploadsOpen(t, store, "swept-bucket")
	if len(held) != 1 || held[0] != "app/fresh.bin" {
		t.Errorf("the store holds %v open, want only the upload this request opened: an upload abandoned a day ago holds its parts on the volume forever", held)
	}
}

func TestASweepReachesNoFurtherThanTheAppsOwnPrefix(t *testing.T) {
	if testing.Short() {
		t.Skip("stands a real store up")
	}
	store := aRunningStore(t, "shared-bucket")
	openUpload(t, store, "shared-bucket", "app/mine.bin")
	openUpload(t, store, "shared-bucket", "other/theirs.bin")

	clock := time.Now().UTC().Add(orphanAge + time.Hour)
	service := sweeping(t, store, "shared-bucket", func() time.Time { return clock })
	service.sweepUploads(context.Background(), scopeOf("shared-bucket/app"))

	held := uploadsOpen(t, store, "shared-bucket")
	if len(held) != 1 || held[0] != "other/theirs.bin" {
		t.Errorf("the store holds %v open: a sweep reaches the keys its app was granted and no others", held)
	}
}

func TestABucketIsSweptAtMostOnceAnHourHoweverOftenItIsWrittenTo(t *testing.T) {
	t.Parallel()

	counted := &countingStore{fakeStore: newFakeStore()}
	service := New(Config{Objects: counted, SweepUploads: true, Granted: []string{"counted/app"}})
	held := time.Now().UTC()
	service.now = func() time.Time { return held }
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

	held = held.Add(61 * time.Minute)
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

func TestASweepTheStoreRefusesLeavesTheUploadItWasOpeningStanding(t *testing.T) {
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
