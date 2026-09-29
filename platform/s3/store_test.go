package s3

import (
	"context"
	"net/http"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider/enginetest"
)

func TestEnsureBucketCreatesABucketOnceAndLetsOnlyTheLatestOriginsUpload(t *testing.T) {
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

	if err := store.EnsureBucket(ctx, "ensured", []string{"http://localhost:3000"}); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}
	if err := store.EnsureBucket(ctx, "ensured", []string{"http://localhost:4100"}); err != nil {
		t.Fatalf("EnsureBucket on a bucket that exists: %v", err)
	}
	if got := allowed("http://localhost:4100"); got != "http://localhost:4100" {
		t.Errorf("Access-Control-Allow-Origin = %q for the origin allowed last", got)
	}
	if got := allowed("http://localhost:3000"); got == "http://localhost:3000" {
		t.Error("an origin no longer allowed may still upload")
	}
}
