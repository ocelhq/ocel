package s3

import (
	"context"
	"errors"
	"sync"
	"testing"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

type flakyPoster struct {
	mu      sync.Mutex
	refuse  int
	posts   []callbackBody
	refused int
}

func (p *flakyPoster) Post(ctx context.Context, url string, body []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refused < p.refuse {
		p.refused++
		return errors.New("the app answered 502 Bad Gateway")
	}
	held := &recordingPoster{}
	if err := held.Post(ctx, url, body); err != nil {
		return err
	}
	p.posts = append(p.posts, held.posts...)
	return nil
}

func TestACallbackTheAppRefusedIsDeliveredOnTheNextComplete(t *testing.T) {
	t.Parallel()

	poster := &flakyPoster{refuse: 1}
	h := newHarness(t, func(cfg *Config) { cfg.Callbacks = poster })
	h.presign(t, "a.png", 3, "image/png")
	h.store.put("store", "a.png", []byte("abc"), "image/png")

	ctx := context.Background()
	if _, err := h.svc.CompleteUpload(ctx, &bucketv1.CompleteUploadRequest{SessionId: "sess_fixed"}); err == nil {
		t.Fatal("a callback the app refused settled the session anyway, so the app never hears about the upload")
	}

	resp, err := h.svc.CompleteUpload(ctx, &bucketv1.CompleteUploadRequest{SessionId: "sess_fixed"})
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if resp.GetState() != bucketv1.UploadState_UPLOAD_STATE_SUCCEEDED {
		t.Fatalf("state = %v, want SUCCEEDED", resp.GetState())
	}
	if len(poster.posts) != 1 || poster.posts[0].File.Key != "a.png" {
		t.Fatalf("callbacks = %+v, want the one the first attempt lost", poster.posts)
	}

	if _, err := h.svc.CompleteUpload(ctx, &bucketv1.CompleteUploadRequest{SessionId: "sess_fixed"}); err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if len(poster.posts) != 1 {
		t.Fatalf("callbacks = %+v, want the app told exactly once", poster.posts)
	}
}
