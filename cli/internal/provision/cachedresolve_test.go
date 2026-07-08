package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/resolvecache"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// withTestCache points openCache at a fresh temp-dir cache for the duration
// of the test, restoring the previous seam afterwards, and returns the
// cache's directory so tests can inspect the on-disk files directly.
func withTestCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := openCache
	openCache = func() (*resolvecache.Cache, error) { return resolvecache.OpenAt(dir) }
	t.Cleanup(func() { openCache = prev })
	return dir
}

// countingResolveServer serves POST /api/resources/resolve with the same
// wire contract the real endpoint serves, counting how many times it's hit
// so tests can assert whether CachedResolve skipped the call.
func countingResolveServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req resolveRequestBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		env := make(map[string]string, len(req.Resources))
		for _, res := range req.Resources {
			env[fmt.Sprintf("OCEL_RESOURCE_%s_%s", res.Type, res.Name)] = fmt.Sprintf(`{"connectionString":"postgres://resolved/%s"}`, res.Name)
		}
		_ = json.NewEncoder(w).Encode(resolveResponseBody{
			Env:       env,
			ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	}))
	t.Cleanup(ts.Close)
	return ts, &calls
}

func TestCachedResolve_MissCallsAPIAndPersistsA0600CacheFile(t *testing.T) {
	dir := withTestCache(t)
	ts, calls := countingResolveServer(t)

	resources := []manifest.Entry{{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES}}
	got, err := CachedResolve(context.Background(), httpClient, ts.URL, "tok", "proj_1", resources)
	if err != nil {
		t.Fatalf("CachedResolve: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("calls = %d, want 1", *calls)
	}
	if len(got) != 1 {
		t.Fatalf("got = %+v", got)
	}

	cache, err := openCache()
	if err != nil {
		t.Fatalf("openCache: %v", err)
	}
	entry, ok := cache.Load("proj_1")
	if !ok {
		t.Fatal("expected a cache entry after a miss, got none")
	}
	if entry.ExpiresAt.IsZero() || len(entry.Env) == 0 {
		t.Fatalf("entry = %+v, want populated Env and ExpiresAt", entry)
	}

	info, err := os.Stat(filepath.Join(dir, "proj_1.json"))
	if err != nil {
		t.Fatalf("stat cache file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("cache file mode = %o, want 0600", perm)
	}
}

func TestCachedResolve_HitSkipsTheAPICallAndReusesEnv(t *testing.T) {
	withTestCache(t)
	ts, calls := countingResolveServer(t)

	resources := []manifest.Entry{{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES}}
	first, err := CachedResolve(context.Background(), httpClient, ts.URL, "tok", "proj_1", resources)
	if err != nil {
		t.Fatalf("CachedResolve (first): %v", err)
	}

	second, err := CachedResolve(context.Background(), httpClient, ts.URL, "tok", "proj_1", resources)
	if err != nil {
		t.Fatalf("CachedResolve (second): %v", err)
	}

	if *calls != 1 {
		t.Fatalf("calls = %d, want 1 (second resolve should have hit the cache)", *calls)
	}
	if second[0].Env["OCEL_RESOURCE_POSTGRES_main"] != first[0].Env["OCEL_RESOURCE_POSTGRES_main"] {
		t.Fatalf("second Env = %+v, want it to match the cached first Env %+v", second[0].Env, first[0].Env)
	}
}

func TestCachedResolve_DefinitionChangeForcesReResolve(t *testing.T) {
	withTestCache(t)
	ts, calls := countingResolveServer(t)

	if _, err := CachedResolve(context.Background(), httpClient, ts.URL, "tok", "proj_1",
		[]manifest.Entry{{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES}}); err != nil {
		t.Fatalf("CachedResolve (first): %v", err)
	}

	// Same project/account, but a second declared resource - the manifest
	// changed, so this must not reuse the cached single-resource entry.
	if _, err := CachedResolve(context.Background(), httpClient, ts.URL, "tok", "proj_1",
		[]manifest.Entry{
			{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
			{Name: "second", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
		}); err != nil {
		t.Fatalf("CachedResolve (second): %v", err)
	}

	if *calls != 2 {
		t.Fatalf("calls = %d, want 2 (definition change should force a re-resolve)", *calls)
	}
}

func TestCachedResolve_ExpiredCacheForcesReResolve(t *testing.T) {
	withTestCache(t)
	ts, calls := countingResolveServer(t)

	resources := []manifest.Entry{{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES}}
	if _, err := CachedResolve(context.Background(), httpClient, ts.URL, "tok", "proj_1", resources); err != nil {
		t.Fatalf("CachedResolve (first): %v", err)
	}

	// Back-date the cached entry's expiry so the next call must re-resolve.
	cache, err := openCache()
	if err != nil {
		t.Fatalf("openCache: %v", err)
	}
	entry, ok := cache.Load("proj_1")
	if !ok {
		t.Fatal("expected a cache entry after the first resolve")
	}
	entry.ExpiresAt = time.Now().Add(-time.Minute)
	if err := cache.Save("proj_1", entry); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := CachedResolve(context.Background(), httpClient, ts.URL, "tok", "proj_1", resources); err != nil {
		t.Fatalf("CachedResolve (second): %v", err)
	}

	if *calls != 2 {
		t.Fatalf("calls = %d, want 2 (an expired entry should force a re-resolve)", *calls)
	}
}

func TestCachedResolve_AccountSwitchForcesReResolve(t *testing.T) {
	withTestCache(t)
	ts, calls := countingResolveServer(t)

	resources := []manifest.Entry{{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES}}
	if _, err := CachedResolve(context.Background(), httpClient, ts.URL, "tok_a", "proj_1", resources); err != nil {
		t.Fatalf("CachedResolve (account A): %v", err)
	}

	// Same project and defs, different token - simulates switching accounts
	// (a re-login issues a new session token).
	if _, err := CachedResolve(context.Background(), httpClient, ts.URL, "tok_b", "proj_1", resources); err != nil {
		t.Fatalf("CachedResolve (account B): %v", err)
	}

	if *calls != 2 {
		t.Fatalf("calls = %d, want 2 (switching accounts should force a re-resolve)", *calls)
	}
}
