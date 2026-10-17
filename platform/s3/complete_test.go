package s3

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

type interleavedStore struct {
	*fakeStore
	once    sync.Once
	between func()
}

func (i *interleavedStore) GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	out, err := i.fakeStore.GetObject(ctx, in, opts...)
	i.once.Do(i.between)
	return out, err
}

func TestAReplicaThatLosesTheRaceToSettleASessionDeletesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.presign(t, "a.png", 3, "image/png")
	h.store.put("store", "a.png", []byte("abc"), "image/png")

	ctx := context.Background()
	interleaved := &interleavedStore{fakeStore: h.store, between: func() {
		if _, err := h.svc.CompleteUpload(ctx, &bucketv1.CompleteUploadRequest{SessionId: "sess_fixed"}); err != nil {
			t.Errorf("the prompt replica's CompleteUpload: %v", err)
		}
	}}
	late := New(Config{Objects: interleaved, Internal: presigner(), Callbacks: h.poster, Sessions: "store", Granted: NewGrantsByOwnName("store")})
	expiredAt := h.now.Add(3 * time.Hour)
	late.now = func() time.Time { return expiredAt }

	resp, err := late.CompleteUpload(ctx, &bucketv1.CompleteUploadRequest{SessionId: "sess_fixed"})
	if err != nil {
		t.Fatalf("the late replica's CompleteUpload: %v", err)
	}
	if _, present := h.store.objects["store"]["a.png"]; !present {
		t.Fatal("a replica whose view of the session was stale deleted the upload the other replica had already told the app about")
	}
	if resp.GetState() != bucketv1.UploadState_UPLOAD_STATE_SUCCEEDED {
		t.Fatalf("state = %v, want SUCCEEDED, the state the session was settled in", resp.GetState())
	}
	if len(h.poster.posts) != 1 {
		t.Fatalf("callbacks = %+v, want the app told once", h.poster.posts)
	}
}
