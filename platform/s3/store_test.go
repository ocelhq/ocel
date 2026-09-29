package s3

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider/enginetest"
)

func TestEnsureBucketCreatesABucketOnceAndSetUploadOriginsLetsOnlyTheLatestOriginsUpload(t *testing.T) {
	ctx := context.Background()
	running := enginetest.SharedObjectStore(t)
	running.ClaimBucket(t, "ensured")
	store := Store{
		Endpoint:        running.Endpoint,
		Region:          running.Region,
		AccessKeyID:     running.AccessKeyID,
		SecretAccessKey: running.SecretKey,
		PathStyle:       true,
	}
	allowed := func(origin string) string {
		req, err := http.NewRequestWithContext(ctx, http.MethodOptions, running.Endpoint+"/ensured/a.txt", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodPut)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("preflight: %v", err)
		}
		_ = resp.Body.Close()
		return resp.Header.Get("Access-Control-Allow-Origin")
	}

	for _, origins := range [][]string{{"http://localhost:3000"}, {"http://localhost:4100"}} {
		if err := store.EnsureBucket(ctx, "ensured"); err != nil {
			t.Fatalf("EnsureBucket: %v", err)
		}
		if err := store.SetUploadOrigins(ctx, "ensured", origins); err != nil {
			t.Fatalf("SetUploadOrigins(%v): %v", origins, err)
		}
	}
	if got := allowed("http://localhost:4100"); got != "http://localhost:4100" {
		t.Errorf("Access-Control-Allow-Origin = %q for the origin allowed last", got)
	}
	if got := allowed("http://localhost:3000"); got == "http://localhost:3000" {
		t.Error("an origin no longer allowed may still upload")
	}
}

type answeredStore struct {
	mu       sync.Mutex
	requests []string
}

func (a *answeredStore) serve(t *testing.T, createAnswer string) Store {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.requests = append(a.requests, r.Method+" "+r.URL.RequestURI())
		a.mu.Unlock()
		if r.Method == http.MethodPut && r.URL.RawQuery == "" && createAnswer != "" {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + createAnswer + `</Code><Message>the bucket exists</Message></Error>`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return Store{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "AKID", SecretAccessKey: "secret", PathStyle: true}
}

func (a *answeredStore) sent() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.requests...)
}

func TestEnsureBucketRefusesABucketAnotherAccountOwns(t *testing.T) {
	var answered answeredStore
	store := answered.serve(t, "BucketAlreadyExists")

	err := store.EnsureBucket(context.Background(), "taken")
	if !errors.Is(err, ErrBucketTaken) {
		t.Fatalf("EnsureBucket() = %v, want ErrBucketTaken", err)
	}
	if !strings.Contains(err.Error(), "taken") {
		t.Errorf("EnsureBucket() = %q, want the bucket named", err)
	}
}

func TestEnsureBucketAcceptsABucketTheseCredentialsAlreadyOwn(t *testing.T) {
	var answered answeredStore
	store := answered.serve(t, "BucketAlreadyOwnedByYou")

	if err := store.EnsureBucket(context.Background(), "mine"); err != nil {
		t.Fatalf("EnsureBucket() = %v, want a bucket already owned accepted", err)
	}
	for _, request := range answered.sent() {
		if strings.Contains(request, "cors") {
			t.Errorf("EnsureBucket sent %q, want the bucket's upload origins left to SetUploadOrigins", request)
		}
	}
}

func TestSetUploadOriginsWithNoOriginsRemovesTheRules(t *testing.T) {
	var answered answeredStore
	store := answered.serve(t, "")

	if err := store.SetUploadOrigins(context.Background(), "mine", nil); err != nil {
		t.Fatalf("SetUploadOrigins(nil) = %v", err)
	}
	sent := answered.sent()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], http.MethodDelete+" /mine?cors") {
		t.Errorf("SetUploadOrigins(nil) sent %v, want the bucket's CORS rules deleted", sent)
	}
}
