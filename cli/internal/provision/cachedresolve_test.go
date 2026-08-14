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
	"github.com/ocelhq/ocel/pkg/naming"
)

func cachingResolver(t *testing.T) (*Resolver, string) {
	t.Helper()
	dir := t.TempDir()
	r := NewResolver()
	r.OpenCache = func() (*resolvecache.Cache, error) { return resolvecache.OpenAt(dir) }
	return r, dir
}

func countingResolveServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req resolveRequestBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
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

func TestCachedResolve(t *testing.T) {
	t.Parallel()

	onePostgres := []manifest.Entry{{Name: "main", Type: naming.TokenPostgres}}

	t.Run("a miss calls the API and persists a 0600 cache file", func(t *testing.T) {
		t.Parallel()
		resolver, dir := cachingResolver(t)
		ts, calls := countingResolveServer(t)

		got, err := resolver.Resolve(context.Background(), ts.URL, "tok", "proj_1", onePostgres)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if *calls != 1 {
			t.Fatalf("calls = %d, want 1", *calls)
		}
		if len(got) != 1 {
			t.Fatalf("got = %+v", got)
		}

		cache, err := resolver.OpenCache()
		if err != nil {
			t.Fatalf("OpenCache: %v", err)
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
	})

	t.Run("a hit skips the API call and reuses env", func(t *testing.T) {
		t.Parallel()
		resolver, _ := cachingResolver(t)
		ts, calls := countingResolveServer(t)

		first, err := resolver.Resolve(context.Background(), ts.URL, "tok", "proj_1", onePostgres)
		if err != nil {
			t.Fatalf("Resolve (first): %v", err)
		}

		second, err := resolver.Resolve(context.Background(), ts.URL, "tok", "proj_1", onePostgres)
		if err != nil {
			t.Fatalf("Resolve (second): %v", err)
		}

		if *calls != 1 {
			t.Fatalf("calls = %d, want 1 (second resolve should have hit the cache)", *calls)
		}
		if second[0].Env["OCEL_RESOURCE_POSTGRES_main"] != first[0].Env["OCEL_RESOURCE_POSTGRES_main"] {
			t.Fatalf("second Env = %+v, want it to match the cached first Env %+v", second[0].Env, first[0].Env)
		}
	})

	t.Run("a definition change forces a re-resolve", func(t *testing.T) {
		t.Parallel()
		resolver, _ := cachingResolver(t)
		ts, calls := countingResolveServer(t)

		if _, err := resolver.Resolve(context.Background(), ts.URL, "tok", "proj_1", onePostgres); err != nil {
			t.Fatalf("Resolve (first): %v", err)
		}

		if _, err := resolver.Resolve(context.Background(), ts.URL, "tok", "proj_1",
			[]manifest.Entry{
				{Name: "main", Type: naming.TokenPostgres},
				{Name: "second", Type: naming.TokenPostgres},
			}); err != nil {
			t.Fatalf("Resolve (second): %v", err)
		}

		if *calls != 2 {
			t.Fatalf("calls = %d, want 2 (definition change should force a re-resolve)", *calls)
		}
	})

	t.Run("an expired entry forces a re-resolve", func(t *testing.T) {
		t.Parallel()
		resolver, _ := cachingResolver(t)
		ts, calls := countingResolveServer(t)

		if _, err := resolver.Resolve(context.Background(), ts.URL, "tok", "proj_1", onePostgres); err != nil {
			t.Fatalf("Resolve (first): %v", err)
		}

		resolver.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }

		if _, err := resolver.Resolve(context.Background(), ts.URL, "tok", "proj_1", onePostgres); err != nil {
			t.Fatalf("Resolve (second): %v", err)
		}

		if *calls != 2 {
			t.Fatalf("calls = %d, want 2 (an expired entry should force a re-resolve)", *calls)
		}
	})

	t.Run("an account switch forces a re-resolve", func(t *testing.T) {
		t.Parallel()
		resolver, _ := cachingResolver(t)
		ts, calls := countingResolveServer(t)

		if _, err := resolver.Resolve(context.Background(), ts.URL, "tok_a", "proj_1", onePostgres); err != nil {
			t.Fatalf("Resolve (account A): %v", err)
		}

		if _, err := resolver.Resolve(context.Background(), ts.URL, "tok_b", "proj_1", onePostgres); err != nil {
			t.Fatalf("Resolve (account B): %v", err)
		}

		if *calls != 2 {
			t.Fatalf("calls = %d, want 2 (switching accounts should force a re-resolve)", *calls)
		}
	})
}
