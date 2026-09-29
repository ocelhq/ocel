package bucket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestACallbackToAnOriginNoBucketAllowsIsDroppedWithoutFailingTheUpload(t *testing.T) {
	var posts atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts.Add(1) }))
	t.Cleanup(foreign.Close)

	err := callbacks{allowed: []string{"http://127.0.0.1:1"}}.Post(context.Background(), foreign.URL+"/api/upload", []byte(`{}`))
	if err != nil {
		t.Fatalf("Post = %v, want nil so the upload still completes", err)
	}
	if got := posts.Load(); got != 0 {
		t.Fatalf("the origin no bucket allows received %d callbacks, want 0", got)
	}
}

func TestACallbackToAnAllowedOriginIsPosted(t *testing.T) {
	var posts atomic.Int32
	allowed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts.Add(1) }))
	t.Cleanup(allowed.Close)

	if err := (callbacks{allowed: []string{allowed.URL}}).Post(context.Background(), allowed.URL+"/api/upload", []byte(`{}`)); err != nil {
		t.Fatalf("Post = %v", err)
	}
	if got := posts.Load(); got != 1 {
		t.Fatalf("the allowed origin received %d callbacks, want 1", got)
	}
}
