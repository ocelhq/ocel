//go:build integration

package s3

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

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
		t.Fatalf("read the uploads the store has open: %v", err)
	}
	keys := make([]string, 0, len(out.Uploads))
	for _, upload := range out.Uploads {
		keys = append(keys, aws.ToString(upload.Key))
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
		t.Skip("runs a real store")
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

	pending := uploadsOpen(t, store, "swept-bucket")
	if len(pending) != 1 || pending[0] != "app/fresh.bin" {
		t.Errorf("the store has %v open, want only the upload this request opened: an upload abandoned a day ago keeps its parts on the volume forever", pending)
	}
}

func TestASweepReachesNoFurtherThanTheAppsOwnPrefix(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real store")
	}
	store := aRunningStore(t, "shared-bucket")
	openUpload(t, store, "shared-bucket", "app/mine.bin")
	openUpload(t, store, "shared-bucket", "other/theirs.bin")

	clock := time.Now().UTC().Add(orphanAge + time.Hour)
	service := sweeping(t, store, "shared-bucket", func() time.Time { return clock })
	service.sweepUploads(context.Background(), scopeOf("shared-bucket/app"))

	pending := uploadsOpen(t, store, "shared-bucket")
	if len(pending) != 1 || pending[0] != "other/theirs.bin" {
		t.Errorf("the store has %v open: a sweep reaches the keys its app was granted and no others", pending)
	}
}
