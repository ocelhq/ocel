package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestBytecodeCacheKey(t *testing.T) {
	cases := []struct {
		name         string
		prefix       string
		functionName string
		nodeVersion  string
		goArch       string
		want         string
	}{
		{
			name:         "amd64 maps to the AWS x86_64 spelling",
			prefix:       "ocel",
			functionName: "my-app",
			nodeVersion:  "24.3.1",
			goArch:       "amd64",
			want:         "ocel/bytecode/my-app/node24.3.1-x86_64.tar.gz",
		},
		{
			name:         "arm64 passes through unchanged",
			prefix:       "ocel",
			functionName: "my-app",
			nodeVersion:  "24.3.1",
			goArch:       "arm64",
			want:         "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz",
		},
		{
			name:         "an unrecognized arch still passes through",
			prefix:       "stg/deploy",
			functionName: "other-fn",
			nodeVersion:  "20.11.0",
			goArch:       "riscv64",
			want:         "stg/deploy/bytecode/other-fn/node20.11.0-riscv64.tar.gz",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bytecodeCacheKey(tc.prefix, tc.functionName, tc.nodeVersion, tc.goArch)
			if got != tc.want {
				t.Errorf("bytecodeCacheKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestS3Arch(t *testing.T) {
	cases := []struct {
		goArch string
		want   string
	}{
		{"amd64", "x86_64"},
		{"arm64", "arm64"},
		{"386", "386"},
	}
	for _, tc := range cases {
		if got := s3Arch(tc.goArch); got != tc.want {
			t.Errorf("s3Arch(%q) = %q, want %q", tc.goArch, got, tc.want)
		}
	}
}

func TestCanonicalNodeVersion(t *testing.T) {
	cases := []struct {
		name    string
		version string
		want    string
		wantErr bool
	}{
		{name: "v-prefixed semver", version: "v24.3.1", want: "24.3.1"},
		{name: "no v prefix", version: "20.11.0", want: "20.11.0"},
		{name: "double digit major", version: "v18.19.1", want: "18.19.1"},
		{name: "empty string", version: "", wantErr: true},
		{name: "major only", version: "24", wantErr: true},
		{name: "major and minor only", version: "24.3", wantErr: true},
		{name: "trailing prerelease tag", version: "v24.3.1-nightly", wantErr: true},
		{name: "not a version at all", version: "not-a-version", wantErr: true},
		{name: "whitespace only", version: "   ", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canonicalNodeVersion(tc.version)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("canonicalNodeVersion(%q) = %q, nil, want an error", tc.version, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("canonicalNodeVersion(%q) unexpected error: %v", tc.version, err)
			}
			if got != tc.want {
				t.Errorf("canonicalNodeVersion(%q) = %q, want %q", tc.version, got, tc.want)
			}
			if strings.HasPrefix(got, "v") {
				t.Errorf("canonicalNodeVersion(%q) = %q, want no leading v", tc.version, got)
			}
		})
	}
}

// readArchive extracts an archive back into a name -> contents map, for tests
// that need to assert on what buildBytecodeArchive actually wrote.
func readArchive(t *testing.T, data []byte) map[string]string {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)

	out := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = string(content)
	}
	return out
}

// TestBuildBytecodeArchive_RoundTrip proves the archive built from a compile
// cache directory reads back with the standard library's own tar+gzip readers
// and preserves the version-hash subdirectory Node nests its output under,
// using relative paths only.
func TestBuildBytecodeArchive_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	versionDir := filepath.Join(dir, "abc123-hash")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(dir, "top-level.bin"):       "top level contents",
		filepath.Join(versionDir, "nested-a.bin"): "nested a contents",
		filepath.Join(versionDir, "nested-b.bin"): "nested b contents",
	}
	for path, contents := range files {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	archive, err := buildBytecodeArchive(context.Background(), dir)
	if err != nil {
		t.Fatalf("buildBytecodeArchive: %v", err)
	}

	got := readArchive(t, archive)
	want := map[string]string{
		"top-level.bin":            "top level contents",
		"abc123-hash/nested-a.bin": "nested a contents",
		"abc123-hash/nested-b.bin": "nested b contents",
	}
	if len(got) != len(want) {
		t.Fatalf("archive has %d entries, want %d: %v", len(got), len(want), got)
	}
	for name, contents := range want {
		if got[name] != contents {
			t.Errorf("archive[%q] = %q, want %q", name, got[name], contents)
		}
	}
}

// TestBuildBytecodeArchive_SkipsNonRegularFiles proves a symlink planted in
// the compile cache directory is left out of the archive rather than
// followed or stored as a link, since only regular files are meant to travel.
func TestBuildBytecodeArchive_SkipsNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.bin")
	if err := os.WriteFile(target, []byte("real contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.bin")); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	archive, err := buildBytecodeArchive(context.Background(), dir)
	if err != nil {
		t.Fatalf("buildBytecodeArchive: %v", err)
	}
	got := readArchive(t, archive)
	if _, ok := got["link.bin"]; ok {
		t.Errorf("archive contains symlink link.bin, want it skipped")
	}
	if _, ok := got["subdir"]; ok {
		t.Errorf("archive contains directory entry subdir, want it skipped")
	}
	if got["real.bin"] != "real contents" {
		t.Errorf("archive[real.bin] = %q, want %q", got["real.bin"], "real contents")
	}
}

// TestBuildBytecodeArchive_MissingDirectoryIsEmptyNotAnError proves a
// function that has not written a compile cache yet (or whose /tmp was wiped)
// gets an empty archive back, not a failure the caller has to distinguish
// from a real error before deciding whether to skip the upload.
func TestBuildBytecodeArchive_MissingDirectoryIsEmptyNotAnError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")

	archive, err := buildBytecodeArchive(context.Background(), dir)
	if err != nil {
		t.Fatalf("buildBytecodeArchive: %v", err)
	}
	got := readArchive(t, archive)
	if len(got) != 0 {
		t.Errorf("archive has %d entries, want 0: %v", len(got), got)
	}
}

func TestExceedsBytecodeCacheCeiling(t *testing.T) {
	cases := []struct {
		name string
		size int64
		want bool
	}{
		{name: "well under the ceiling", size: 1024, want: false},
		{name: "exactly at the ceiling", size: bytecodeCacheCeiling, want: false},
		{name: "one byte over the ceiling", size: bytecodeCacheCeiling + 1, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exceedsBytecodeCacheCeiling(tc.size); got != tc.want {
				t.Errorf("exceedsBytecodeCacheCeiling(%d) = %v, want %v", tc.size, got, tc.want)
			}
		})
	}
}

// fakeBytecodeStore stands in for S3, recording what the upload asked of it so
// a test can assert on the exact bucket and key rather than on the fact that a
// method was called.
type fakeBytecodeStore struct {
	mu      sync.Mutex
	exists  bool
	headErr error
	putErr  error
	heads   []string
	puts    []fakePut

	getBody io.ReadCloser
	getSize int64
	getErr  error
	gets    []string
}

type fakePut struct {
	bucket string
	key    string
	body   []byte
}

func (f *fakeBytecodeStore) objectExists(_ context.Context, bucket, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads = append(f.heads, bucket+"/"+key)
	return f.exists, f.headErr
}

func (f *fakeBytecodeStore) putObject(_ context.Context, bucket, key string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, fakePut{bucket: bucket, key: key, body: body})
	return f.putErr
}

func (f *fakeBytecodeStore) getObject(_ context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets = append(f.gets, bucket+"/"+key)
	if f.getErr != nil {
		return nil, 0, f.getErr
	}
	return f.getBody, f.getSize, nil
}

// cacheDirWith writes a compile cache directory with one file in it and
// returns the path, which is what node's flush ack would name.
func cacheDirWith(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cached.blob"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// uploadFixture assembles an upload whose flush ack and S3 are both under the
// test's control, wired to a fixed bucket and key exactly as the resolution
// would hand them to it, and reports how many times the flush was asked for.
func uploadFixture(store bytecodeStore, ack compileCacheFlushedPayload, ackOK bool) (*bytecodeUpload, *int) {
	flushes := 0
	u := &bytecodeUpload{
		store:  store,
		bucket: "assets-xyz",
		key:    "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz",
		root:   ack.Dir,
		flush: func(context.Context) (compileCacheFlushedPayload, bool) {
			flushes++
			return ack, ackOK
		},
	}
	return u, &flushes
}

// What node reports from getCompileCacheDir is not NODE_COMPILE_CACHE but a
// subdirectory of it, named for the node version, architecture, a hash of the
// V8 flags in force and the uid. The read leg extracts into the root, so an
// archive rooted at what node reported arrives one directory above where node
// looks for it and every entry misses — the whole cache delivered, none of it
// read.
func TestBytecodeUpload_ArchiveKeepsTheSubdirectoryNodeReports(t *testing.T) {
	root := t.TempDir()
	nodeDir := filepath.Join(root, "v24.3.1-arm64-9ac5647c-993")
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodeDir, "cached.blob"), []byte("compiled bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := &fakeBytecodeStore{}
	u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: nodeDir, OK: true}, true)
	u.root = root

	u.run(context.Background())

	if len(store.puts) != 1 {
		t.Fatalf("puts = %d, want 1", len(store.puts))
	}
	got := readArchive(t, store.puts[0].body)
	const want = "v24.3.1-arm64-9ac5647c-993/cached.blob"
	if got[want] != "compiled bytes" {
		t.Errorf("uploaded archive = %v, want an entry at %s", got, want)
	}
}

func TestBytecodeUpload_PutsToTheGivenBucketAndKey(t *testing.T) {
	dir := cacheDirWith(t, "compiled bytes")
	store := &fakeBytecodeStore{}
	u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)

	u.run(context.Background())

	if len(store.heads) != 1 || store.heads[0] != "assets-xyz/ocel/bytecode/my-app/node24.3.1-arm64.tar.gz" {
		t.Fatalf("heads = %v, want a single head of the given key", store.heads)
	}
	if len(store.puts) != 1 {
		t.Fatalf("puts = %d, want 1", len(store.puts))
	}
	put := store.puts[0]
	if put.bucket != "assets-xyz" || put.key != "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz" {
		t.Errorf("put to %s/%s, want assets-xyz/ocel/bytecode/my-app/node24.3.1-arm64.tar.gz", put.bucket, put.key)
	}
	if got := readArchive(t, put.body); got["cached.blob"] != "compiled bytes" {
		t.Errorf("uploaded archive = %v, want it to carry the cache directory's contents", got)
	}
}

// The HEAD runs before the flush, so an instance that lost the upload race
// finds out for the price of a HEAD alone: no flush, and so no archive.
func TestBytecodeUpload_SkipsThePutWhenTheObjectAlreadyExists(t *testing.T) {
	dir := cacheDirWith(t, "compiled bytes")
	store := &fakeBytecodeStore{exists: true}
	u, flushes := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)

	u.run(context.Background())

	if len(store.heads) != 1 {
		t.Errorf("heads = %v, want the key to have been checked", store.heads)
	}
	if len(store.puts) != 0 {
		t.Errorf("puts = %v, want none once the object exists", store.puts)
	}
	if *flushes != 0 {
		t.Errorf("flushes = %d, want node never asked to flush once the object exists", *flushes)
	}
}

// A flush that node could not honour — an old runtime, or no cache written —
// means there is nothing on disk worth uploading, so the PUT never happens.
// The HEAD still runs: it is checked before the flush is even asked for.
func TestBytecodeUpload_SkipsWhenTheFlushAckIsNotOK(t *testing.T) {
	cases := []struct {
		name  string
		ack   compileCacheFlushedPayload
		ackOK bool
	}{
		{name: "node answered not-ok", ack: compileCacheFlushedPayload{OK: false}, ackOK: true},
		{name: "node never answered", ackOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeBytecodeStore{}
			u, _ := uploadFixture(store, tc.ack, tc.ackOK)

			u.run(context.Background())

			if len(store.heads) != 1 {
				t.Errorf("heads = %v, want the key checked before the flush was asked for", store.heads)
			}
			if len(store.puts) != 0 {
				t.Errorf("puts = %v, want nothing without a usable flush", store.puts)
			}
		})
	}
}

// The deadline is already inside the margin the runtime needs to call /next, so
// there is no time to spend and the attempt never starts — not even the flush,
// which would hold the loop open on a child that owes the platform an answer.
func TestBytecodeUpload_SkipsEntirelyWhenTheBudgetIsNonPositive(t *testing.T) {
	store := &fakeBytecodeStore{}
	u, flushes := uploadFixture(store, compileCacheFlushedPayload{Dir: t.TempDir(), OK: true}, true)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(completionMargin/2))
	defer cancel()
	u.run(ctx)

	if *flushes != 0 {
		t.Errorf("flushes = %d, want the child left alone", *flushes)
	}
	if len(store.heads) != 0 || len(store.puts) != 0 {
		t.Errorf("touched S3 (heads=%v puts=%v), want nothing", store.heads, store.puts)
	}
}

// An empty cache directory is the shape of a function whose /tmp was wiped or
// whose flush produced nothing; uploading an empty archive would poison every
// later instance that rehydrated from it.
func TestBytecodeUpload_SkipsAnEmptyCacheDirectory(t *testing.T) {
	store := &fakeBytecodeStore{}
	u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: t.TempDir(), OK: true}, true)

	u.run(context.Background())

	if len(store.heads) != 1 {
		t.Errorf("heads = %v, want the key checked before the empty cache was found", store.heads)
	}
	if len(store.puts) != 0 {
		t.Errorf("puts = %v, want nothing to upload", store.puts)
	}
}

// An oversized cache is rejected off the stat pass, so nothing is read or
// compressed on the way to the decision — which is why a sparse file the size of
// the ceiling costs this test nothing.
func TestBytecodeUpload_SkipsWhenTheCacheIsOverTheCeiling(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "big.blob"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(bytecodeCacheCeiling + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	store := &fakeBytecodeStore{}
	u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)

	start := time.Now()
	u.run(context.Background())

	if len(store.heads) != 1 {
		t.Errorf("heads = %v, want the key checked before the ceiling was decided", store.heads)
	}
	if len(store.puts) != 0 {
		t.Errorf("puts = %v, want nothing over the ceiling", store.puts)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s, want the ceiling decided before anything was read or compressed", elapsed)
	}
}

// Nothing S3 says can reach the invocation, which has already been answered by
// the time this runs. A failure is a log line and the end of the attempt.
func TestBytecodeUpload_SwallowsStoreErrors(t *testing.T) {
	cases := []struct {
		name  string
		store *fakeBytecodeStore
	}{
		{name: "head fails", store: &fakeBytecodeStore{headErr: errors.New("access denied")}},
		{name: "put fails", store: &fakeBytecodeStore{putErr: errors.New("slow down")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := uploadFixture(tc.store, compileCacheFlushedPayload{Dir: cacheDirWith(t, "x"), OK: true}, true)
			u.run(context.Background()) // must simply return
		})
	}
}

// The same context bounds the HEAD and the PUT, so whichever one S3 hangs on is
// cancelled at the budget rather than running past the deadline the platform is
// about to enforce.
//
// The budget is spent from inside the blocked call rather than by a wall clock
// the flush, the stat pass and the gzip ahead of it have to beat: the store is
// reached first and the budget ends second, however slow the box is.
func TestBytecodeUpload_BoundsBothStoreCallsByTheBudget(t *testing.T) {
	for _, blockOn := range []string{"head", "put"} {
		t.Run(blockOn, func(t *testing.T) {
			ctx, spendBudget := context.WithCancel(context.Background())
			defer spendBudget()

			store := &blockingBytecodeStore{blockOn: blockOn, spendBudget: spendBudget}
			u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: cacheDirWith(t, "x"), OK: true}, true)

			done := make(chan struct{})
			go func() { defer close(done); u.run(ctx) }()

			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatalf("run never returned, so the %s outlived the budget", blockOn)
			}
			if !store.reached {
				t.Errorf("the %s was never reached, so the test proves nothing", blockOn)
			}
		})
	}
}

// blockingBytecodeStore hangs on one of the two calls until the context it was
// handed ends it, and passes the other through, so each leg's bounding can be
// proven on its own. Reaching the blocked call is what spends the budget, so a
// call handed a context not derived from the caller's never returns — which is
// exactly the failure the test is looking for.
type blockingBytecodeStore struct {
	blockOn     string
	spendBudget context.CancelFunc
	reached     bool
}

func (b *blockingBytecodeStore) block(ctx context.Context) error {
	b.reached = true
	b.spendBudget()
	<-ctx.Done()
	return ctx.Err()
}

func (b *blockingBytecodeStore) objectExists(ctx context.Context, _, _ string) (bool, error) {
	if b.blockOn != "head" {
		return false, nil
	}
	return false, b.block(ctx)
}

func (b *blockingBytecodeStore) putObject(ctx context.Context, _, _ string, _ []byte) error {
	if b.blockOn != "put" {
		return nil
	}
	return b.block(ctx)
}

func (b *blockingBytecodeStore) getObject(context.Context, string, string) (io.ReadCloser, int64, error) {
	return nil, 0, errBytecodeCacheMiss
}

// A budget already spent by the time flush returns stops the attempt at the
// very next ctx-aware step: compileCacheSize's walk sees ctx.Err() on its
// first callback (the root directory entry, visited before any file) and run
// gives up there, never reaching the archive build. buildArchiveWithin's own
// pre-check — a budget spent before *it* specifically starts — is pinned
// directly by TestBuildArchiveWithin_AbandonsOnAnExpiredContext, since
// compileCacheSize always intercepts an already-expired context first when
// reached through run: there is no way to spend the budget between the two
// without a hook this package doesn't have.
func TestBytecodeUpload_AbandonsBeforeMeasuringTheCacheWhenTheBudgetIsAlreadySpent(t *testing.T) {
	dir := cacheDirWith(t, "compiled bytes")
	store := &fakeBytecodeStore{}
	u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)

	u.flush = func(ctx context.Context) (compileCacheFlushedPayload, bool) {
		<-ctx.Done()
		return compileCacheFlushedPayload{Dir: dir, OK: true}, true
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(completionMargin+100*time.Millisecond))
	defer cancel()

	start := time.Now()
	u.run(ctx)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s, want the attempt abandoned at the budget", elapsed)
	}
	if len(store.puts) != 0 {
		t.Errorf("puts = %v, want none once the budget is spent", store.puts)
	}
}

// bigCacheDir writes a cache directory large enough that archiving it takes long
// enough to be cancelled partway, with incompressible contents so gzip cannot
// shortcut the work. It returns the directory and how long a full build of it
// costs, so a test can cancel at a fraction of that rather than at a wall-clock
// guess that would differ between machines and between -race and not.
func bigCacheDir(t *testing.T) (string, time.Duration) {
	t.Helper()
	dir := t.TempDir()
	buf := make([]byte, 8<<10)
	for i := range 400 {
		rand.Read(buf)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d.blob", i)), buf, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	start := time.Now()
	if _, err := buildBytecodeArchive(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	baseline := time.Since(start)
	if baseline < 10*time.Millisecond {
		t.Skipf("archiving is too fast here (%s) to cancel partway reliably", baseline)
	}
	return dir, baseline
}

// The walk has to stop, not just the wait on it. A goroutine left compressing
// after the loop moved on resumes when Lambda thaws the sandbox and competes
// with a later request for the CPU this feature exists to save.
func TestBuildBytecodeArchive_StopsTheWalkWhenTheContextEnds(t *testing.T) {
	dir, baseline := bigCacheDir(t)

	ctx, cancel := context.WithTimeout(context.Background(), baseline/5)
	defer cancel()

	start := time.Now()
	_, err := buildBytecodeArchive(ctx, dir)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("buildBytecodeArchive() error = %v, want the context's", err)
	}
	if elapsed > baseline/2 {
		t.Errorf("took %s against a %s full build, want the walk stopped partway", elapsed, baseline)
	}
}

func TestBuildArchiveWithin_AbandonsOnAnExpiredContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := buildArchiveWithin(ctx, cacheDirWith(t, "compiled bytes"))
	if err == nil {
		t.Fatal("buildArchiveWithin() error = nil, want the budget reported")
	}
	if !strings.Contains(err.Error(), "no budget left") {
		t.Errorf("error = %q, want the pre-check's, since the budget was spent before the call", err)
	}
}

// The budget is live on entry and expires during the build, which is the
// scenario the select exists for — distinct from the pre-check above, and told
// apart from it by which error comes back.
func TestBuildArchiveWithin_AbandonsABuildAlreadyInFlight(t *testing.T) {
	dir, baseline := bigCacheDir(t)

	ctx, cancel := context.WithTimeout(context.Background(), baseline/5)
	defer cancel()

	start := time.Now()
	_, err := buildArchiveWithin(ctx, dir)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("buildArchiveWithin() error = nil, want the build abandoned")
	}
	if !strings.Contains(err.Error(), "outlasted the upload budget") {
		t.Errorf("error = %q, want the select's, since the budget was live on entry", err)
	}
	if elapsed > baseline/2 {
		t.Errorf("took %s against a %s full build, want the caller released partway", elapsed, baseline)
	}
}

// The same thing through run: the upload reaches the build with budget in hand,
// loses it mid-build, and gives up rather than uploading or overrunning.
func TestBytecodeUpload_AbandonsAnArchiveBuildInFlight(t *testing.T) {
	dir, baseline := bigCacheDir(t)
	store := &fakeBytecodeStore{}
	u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(completionMargin+baseline/5))
	defer cancel()

	start := time.Now()
	u.run(ctx)
	elapsed := time.Since(start)

	if len(store.puts) != 0 {
		t.Errorf("puts = %v, want nothing once the build was abandoned", store.puts)
	}
	if elapsed > baseline {
		t.Errorf("took %s against a %s full build, want the attempt abandoned partway", elapsed, baseline)
	}
}

func TestBuildArchiveWithin_ReturnsTheArchiveWithinBudget(t *testing.T) {
	archive, err := buildArchiveWithin(context.Background(), cacheDirWith(t, "compiled bytes"))
	if err != nil {
		t.Fatalf("buildArchiveWithin: %v", err)
	}
	if got := readArchive(t, archive); got["cached.blob"] != "compiled bytes" {
		t.Errorf("archive = %v, want the cache directory's contents", got)
	}
}

func TestCompileCacheSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.blob"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "b.blob"), []byte("678"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := compileCacheSize(context.Background(), dir)
	if err != nil {
		t.Fatalf("compileCacheSize: %v", err)
	}
	if want := int64(8 + 2*tarEntryOverhead); got != want {
		t.Errorf("compileCacheSize() = %d, want %d (payload plus tarEntryOverhead per file, matching untarInto's charge)", got, want)
	}

	missing, err := compileCacheSize(context.Background(), filepath.Join(dir, "does-not-exist"))
	if err != nil {
		t.Fatalf("compileCacheSize on a missing dir: %v", err)
	}
	if missing != 0 {
		t.Errorf("compileCacheSize(missing) = %d, want 0", missing)
	}
}

// The stat pass is bounded like every other leg: a budget already spent stops it
// at the first entry rather than walking a cache directory the attempt can no
// longer do anything with.
func TestCompileCacheSize_StopsOnAnExpiredContext(t *testing.T) {
	dir := cacheDirWith(t, "compiled bytes")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := compileCacheSize(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Errorf("compileCacheSize() error = %v, want the context's", err)
	}
}

func TestUploadBytecodeCacheOnce_RunsAtMostOncePerInstance(t *testing.T) {
	dir := cacheDirWith(t, "compiled bytes")
	store := &fakeBytecodeStore{}
	u, flushes := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)
	m := &Membrane{bytecode: u}

	for range 3 {
		m.uploadBytecodeCacheOnce(context.Background())
	}

	if *flushes != 1 {
		t.Errorf("flushes = %d, want 1", *flushes)
	}
	if len(store.puts) != 1 {
		t.Errorf("puts = %d, want 1", len(store.puts))
	}
}

// A failed attempt is still an attempt: the instance has shown it cannot
// publish, and retrying would spend the next invocation's billed time on it.
func TestUploadBytecodeCacheOnce_DoesNotRetryAfterAFailure(t *testing.T) {
	store := &fakeBytecodeStore{putErr: errors.New("access denied")}
	u, flushes := uploadFixture(store, compileCacheFlushedPayload{Dir: cacheDirWith(t, "x"), OK: true}, true)
	m := &Membrane{bytecode: u}

	m.uploadBytecodeCacheOnce(context.Background())
	m.uploadBytecodeCacheOnce(context.Background())

	if *flushes != 1 {
		t.Errorf("flushes = %d, want 1", *flushes)
	}
}

func TestUploadBytecodeCacheOnce_NoOpsForAnUnconfiguredFunction(t *testing.T) {
	m := &Membrane{}
	m.uploadBytecodeCacheOnce(context.Background()) // must not panic
}

func TestCompileCacheEnv(t *testing.T) {
	t.Run("gate closed", func(t *testing.T) {
		t.Setenv(bytecodePrefixEnvVar, "")
		if got := compileCacheEnv(); got != nil {
			t.Errorf("compileCacheEnv() = %v, want nil with no prefix configured", got)
		}
	})
	t.Run("gate open", func(t *testing.T) {
		t.Setenv(bytecodePrefixEnvVar, "ocel")
		want := []string{"NODE_COMPILE_CACHE=/tmp/.ocel/compile-cache"}
		got := compileCacheEnv()
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("compileCacheEnv() = %v, want %v", got, want)
		}
	})
}

// The gate and its neighbours are read before the node version is even
// asked for, so a function missing one of them never execs anything or
// constructs an AWS client.
func TestResolveBytecodeResolution_NilWhenNotFullyConfigured(t *testing.T) {
	cases := []struct {
		name     string
		prefix   string
		bucket   string
		function string
	}{
		{name: "no prefix", bucket: "assets", function: "my-app"},
		{name: "no bucket", prefix: "ocel", function: "my-app"},
		{name: "no function name", prefix: "ocel", bucket: "assets"},
	}
	nodeVersion := func(context.Context) (string, error) { return "v24.3.1", nil }
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(bytecodePrefixEnvVar, tc.prefix)
			t.Setenv("OCEL_ISR_BUCKET", tc.bucket)
			t.Setenv("AWS_LAMBDA_FUNCTION_NAME", tc.function)
			if got := resolveBytecodeResolution(context.Background(), nodeVersion); got != nil {
				t.Errorf("resolveBytecodeResolution() = %+v, want nil", got)
			}
		})
	}
}

// A version this process cannot read, or one that doesn't parse, disables
// the resolution the same way a missing env var does: there is no key to
// compose without it, so neither leg can be handed one.
func TestResolveBytecodeResolution_NilWhenTheNodeVersionCannotBeRead(t *testing.T) {
	t.Setenv(bytecodePrefixEnvVar, "ocel")
	t.Setenv("OCEL_ISR_BUCKET", "assets")
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "my-app")

	cases := []struct {
		name    string
		version string
		err     error
	}{
		{name: "the binary would not run", err: errors.New("exec format error")},
		{name: "the output is not a version", version: "not-a-version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodeVersion := func(context.Context) (string, error) { return tc.version, tc.err }
			if got := resolveBytecodeResolution(context.Background(), nodeVersion); got != nil {
				t.Errorf("resolveBytecodeResolution() = %+v, want nil", got)
			}
		})
	}
}

// A fully configured function gets a resolution composed from exactly what
// the environment named and the version prober reported. Landing the
// prefix, the bucket, the function name or the version in the wrong place
// would compose a key nothing ever reads back, which no later leg could
// detect — this is the test that pins the composition down.
func TestResolveBytecodeResolution_CarriesTheEnvironmentAndVersionIntoTheKey(t *testing.T) {
	t.Setenv(bytecodePrefixEnvVar, "ocel/stg")
	t.Setenv("OCEL_ISR_BUCKET", "assets-xyz")
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "my-app")
	t.Setenv("AWS_REGION", "us-east-1")

	nodeVersion := func(context.Context) (string, error) { return "v24.3.1", nil }

	r := resolveBytecodeResolution(context.Background(), nodeVersion)
	if r == nil {
		t.Fatal("resolveBytecodeResolution() = nil, want a resolution for a fully configured function")
	}
	if r.bucket != "assets-xyz" {
		t.Errorf("bucket = %q, want %q", r.bucket, "assets-xyz")
	}
	if r.store == nil {
		t.Error("store = nil, want an S3-backed store")
	}
	want := "ocel/stg/bytecode/my-app/node24.3.1-" + s3Arch(runtime.GOARCH) + ".tar.gz"
	if r.key != want {
		t.Errorf("key = %q, want %q", r.key, want)
	}
}

// upload must carry the resolution's own bucket and key rather than
// recompute them, which is what makes the two legs diverging structurally
// impossible rather than merely correct today.
func TestBytecodeResolution_UploadCarriesTheSameBucketAndKey(t *testing.T) {
	r := &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "assets-xyz", key: "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz"}
	flushed := false
	flush := func(context.Context) (compileCacheFlushedPayload, bool) {
		flushed = true
		return compileCacheFlushedPayload{}, false
	}

	u := r.upload(flush)
	if u.store != r.store {
		t.Error("store is not the resolution's")
	}
	if u.bucket != r.bucket {
		t.Errorf("bucket = %q, want the resolution's %q", u.bucket, r.bucket)
	}
	if u.key != r.key {
		t.Errorf("key = %q, want the resolution's %q", u.key, r.key)
	}
	if u.flush == nil {
		t.Fatal("flush = nil, want the caller's")
	}
	u.flush(context.Background())
	if !flushed {
		t.Error("flush is not the one the caller passed")
	}
}

// The upload runs strictly after awaitCompletion returns and strictly before the
// loop is free to call /next. That ordering is the entire basis for the claim
// that publishing the cache costs billed duration rather than request latency.
func TestHandleInvocation_UploadsAfterCompletionAndBeforeTheNextNext(t *testing.T) {
	node := okNode(t)
	rt, _ := fakeRuntime(t, []byte(getEvent))

	goSide, jsSide := net.Pipe()
	t.Cleanup(func() { goSide.Close(); jsSide.Close() })

	store := &fakeBytecodeStore{}
	u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: cacheDirWith(t, "compiled bytes"), OK: true}, true)
	m := &Membrane{
		nodePort: portOf(t, node),
		client:   &http.Client{},
		control:  goSide,
		pending:  map[string]chan struct{}{},
		bytecode: u,
	}
	go m.drainControl(bufio.NewReader(goSide))

	done := make(chan error, 1)
	go func() { done <- handleInvocation(context.Background(), rt, m) }()

	// The response is delivered by now but completion has not fired, so nothing
	// may have been published yet: an upload here would be on someone's latency.
	time.Sleep(75 * time.Millisecond)
	store.mu.Lock()
	early := len(store.puts)
	store.mu.Unlock()
	if early != 0 {
		t.Fatalf("puts before invocation-complete = %d, want the upload held until the invocation is done", early)
	}

	if _, err := jsSide.Write([]byte(`{"type":"invocation-complete","payload":{"requestId":"req-1"}}` + "\n")); err != nil {
		t.Fatalf("write control message: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handleInvocation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handleInvocation never returned")
	}

	// Returning is the loop's only licence to call /next, so a put recorded by
	// now is one that landed before the next invocation could be pulled.
	if len(store.puts) != 1 {
		t.Errorf("puts = %d, want the cache published before the loop moved on", len(store.puts))
	}
}

// controlConnPair wires a Membrane to a stand-in for the node child over a real
// connection, with the drain loop running exactly as startNode runs it. The
// membrane is fully built before the drain starts, since the loop reads the
// fields it was given.
func controlConnPair(t *testing.T) (*Membrane, *bufio.Reader, net.Conn) {
	t.Helper()
	membraneSide, nodeSide := net.Pipe()
	t.Cleanup(func() { membraneSide.Close(); nodeSide.Close() })
	m := &Membrane{control: membraneSide, pending: map[string]chan struct{}{}}
	go m.drainControl(bufio.NewReader(membraneSide))
	return m, bufio.NewReader(nodeSide), nodeSide
}

type flushOutcome struct {
	ack compileCacheFlushedPayload
	ok  bool
}

// startFlush runs the flush off the test goroutine so the test itself can play
// node — reading the request and answering it — with every assertion staying
// where t.Fatal and t.Errorf are legal.
func startFlush(m *Membrane, ctx context.Context) <-chan flushOutcome {
	done := make(chan flushOutcome, 1)
	go func() {
		ack, ok := m.flushCompileCache(ctx)
		done <- flushOutcome{ack: ack, ok: ok}
	}()
	return done
}

func TestFlushCompileCache_DeliversTheAckToTheWaiter(t *testing.T) {
	m, nodeReader, nodeConn := controlConnPair(t)
	done := startFlush(m, context.Background())

	line, err := nodeReader.ReadString('\n')
	if err != nil {
		t.Fatalf("node never received the flush request: %v", err)
	}
	if line != flushCompileCacheLine {
		t.Errorf("node received %q, want %q", line, flushCompileCacheLine)
	}
	fmt.Fprintln(nodeConn, `{"type":"compile-cache-flushed","payload":{"dir":"/tmp/.ocel/compile-cache","ok":true}}`)

	got := <-done
	if !got.ok {
		t.Fatal("flushCompileCache() ok = false, want the ack delivered")
	}
	if got.ack.Dir != "/tmp/.ocel/compile-cache" || !got.ack.OK {
		t.Errorf("ack = %+v, want the dir and ok node reported", got.ack)
	}
}

// Node reports a null dir when it has no compile cache API at all, which must
// read as not-ok rather than as a directory named "null".
func TestFlushCompileCache_CarriesANullDirAsNotOK(t *testing.T) {
	m, nodeReader, nodeConn := controlConnPair(t)
	done := startFlush(m, context.Background())

	if _, err := nodeReader.ReadString('\n'); err != nil {
		t.Fatalf("node never received the flush request: %v", err)
	}
	fmt.Fprintln(nodeConn, `{"type":"compile-cache-flushed","payload":{"dir":null,"ok":false}}`)

	got := <-done
	if !got.ok {
		t.Fatal("flushCompileCache() ok = false, want the ack delivered")
	}
	if got.ack.Dir != "" || got.ack.OK {
		t.Errorf("ack = %+v, want an empty dir and ok=false", got.ack)
	}
}

// A wedged child must not hold the runtime loop off /next, so the wait ends on
// the caller's context even before the flush cap would fire.
func TestFlushCompileCache_GivesUpWhenTheChildNeverAnswers(t *testing.T) {
	m, nodeReader, _ := controlConnPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	done := startFlush(m, ctx)
	if _, err := nodeReader.ReadString('\n'); err != nil {
		t.Fatalf("node never received the flush request: %v", err)
	}

	if got := <-done; got.ok {
		t.Error("flushCompileCache() ok = true, want a give-up")
	}
	if elapsed := time.Since(start); elapsed > compileCacheFlushTimeout {
		t.Errorf("took %s, want the context to end the wait early", elapsed)
	}
}

// A child that never drains its receive buffer must not block the write either.
// Nothing reads the node side here, so only the write deadline can end it.
func TestFlushCompileCache_GivesUpWhenTheChildNeverReadsTheRequest(t *testing.T) {
	m, _, _ := controlConnPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, ok := m.flushCompileCache(ctx); ok {
		t.Error("flushCompileCache() ok = true, want a give-up")
	}
	if elapsed := time.Since(start); elapsed > compileCacheFlushTimeout {
		t.Errorf("took %s, want the write deadline to end the attempt", elapsed)
	}
}

// The deadline is the flush's alone: live values are pushed down this same
// connection, and a deadline left behind would fail those writes forever.
func TestFlushCompileCache_ClearsTheWriteDeadlineAfterwards(t *testing.T) {
	m, nodeReader, _ := controlConnPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := startFlush(m, ctx)
	if _, err := nodeReader.ReadString('\n'); err != nil {
		t.Fatalf("node never received the flush request: %v", err)
	}
	<-done

	// Well past the flush's expired deadline, a later write must still go out.
	time.Sleep(100 * time.Millisecond)
	written := make(chan error, 1)
	go func() {
		_, err := m.control.Write([]byte("{\"type\":\"liveValues\"}\n"))
		written <- err
	}()
	if _, err := nodeReader.ReadString('\n'); err != nil {
		t.Fatalf("a later push never arrived: %v", err)
	}
	if err := <-written; err != nil {
		t.Errorf("a later push failed on the flush's stale deadline: %v", err)
	}
}

func TestFlushCompileCache_NoOpsWithoutAControlConnection(t *testing.T) {
	m := &Membrane{}
	if _, ok := m.flushCompileCache(context.Background()); ok {
		t.Error("flushCompileCache() ok = true, want false with no child attached")
	}
}

// An ack nobody is waiting for (the waiter timed out, or node volunteered one)
// must not wedge the drain loop, which also carries invocation-complete.
func TestDrainControl_DropsAnUnawaitedFlushAck(t *testing.T) {
	m, _, nodeConn := controlConnPair(t)
	waiter := m.registerWaiter("req-1")

	fmt.Fprintln(nodeConn, `{"type":"compile-cache-flushed","payload":{"dir":"/tmp/x","ok":true}}`)
	fmt.Fprintln(nodeConn, `{"type":"invocation-complete","payload":{"requestId":"req-1"}}`)

	select {
	case <-waiter:
	case <-time.After(2 * time.Second):
		t.Fatal("drain loop stalled on an ack with no waiter")
	}
}

func TestBytecodeBudget(t *testing.T) {
	t.Run("no deadline yields the cap", func(t *testing.T) {
		if got := bytecodeBudget(context.Background()); got != bytecodeUploadBudget {
			t.Errorf("bytecodeBudget() = %s, want %s", got, bytecodeUploadBudget)
		}
	})
	t.Run("a distant deadline is capped", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
		defer cancel()
		if got := bytecodeBudget(ctx); got != bytecodeUploadBudget {
			t.Errorf("bytecodeBudget() = %s, want %s", got, bytecodeUploadBudget)
		}
	})
	t.Run("a near deadline leaves the completion margin", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(completionMargin+time.Second))
		defer cancel()
		got := bytecodeBudget(ctx)
		if got <= 0 || got > time.Second {
			t.Errorf("bytecodeBudget() = %s, want a positive budget under 1s", got)
		}
	})
	t.Run("a deadline inside the margin yields nothing", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(completionMargin/2))
		defer cancel()
		if got := bytecodeBudget(ctx); got > 0 {
			t.Errorf("bytecodeBudget() = %s, want a non-positive budget", got)
		}
	})
}

// The gate has to reach the child, not just compute the right string.
func TestNodeChildEnv_CarriesTheCompileCacheOnlyWhenGated(t *testing.T) {
	const want = "NODE_COMPILE_CACHE=/tmp/.ocel/compile-cache"

	t.Run("gate open", func(t *testing.T) {
		t.Setenv(bytecodePrefixEnvVar, "ocel")
		env := nodeChildEnv("/tmp/ocel-control.sock", []string{"OCEL_BAKED_X=1"})
		if !slices.Contains(env, want) {
			t.Errorf("child env has no %q: %v", want, env)
		}
		if !slices.Contains(env, "OCEL_CONTROL_SOCKET=/tmp/ocel-control.sock") {
			t.Error("child env lost the control socket")
		}
		if !slices.Contains(env, "OCEL_BAKED_X=1") {
			t.Error("child env lost the caller's extra entries")
		}
	})

	t.Run("gate closed", func(t *testing.T) {
		t.Setenv(bytecodePrefixEnvVar, "")
		for _, e := range nodeChildEnv("/tmp/ocel-control.sock", nil) {
			if strings.HasPrefix(e, "NODE_COMPILE_CACHE=") {
				t.Errorf("child env has %q with the gate closed", e)
			}
		}
	})
}

// s3Store drives the real s3BytecodeStore against a loopback stand-in for S3, so
// the SDK's own error shapes reach the mapping under test. Static credentials
// keep it away from the environment, and the endpoint keeps it off the network.
func s3Store(t *testing.T, handler http.HandlerFunc) (bytecodeStore, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := s3.New(s3.Options{
		Region:           "us-east-1",
		BaseEndpoint:     &srv.URL,
		UsePathStyle:     true,
		Credentials:      credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
		RetryMaxAttempts: 1, // the budget bounds retries in production; here they are only latency
	})
	return s3BytecodeStore{client: client}, srv
}

// A 404 from HEAD is the answer "upload it", not a failure. Getting this wrong
// in either direction silently disables the feature for good: an error reads as
// "skip", and a false "exists" skips every PUT forever.
func TestS3BytecodeStore_ObjectExists(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantExists bool
		wantErr    bool
	}{
		{name: "404 means the object is absent", status: http.StatusNotFound, wantExists: false},
		{name: "200 means it is already there", status: http.StatusOK, wantExists: true},
		// S3 only answers 404 for a missing key when the caller may also list
		// the bucket. The deployed function's role holds s3:GetObject and
		// s3:PutObject on its own prefix and no s3:ListBucket (isrPolicy), so
		// 403 is how an absent key actually reaches this code in production —
		// reading it as a fault made every fresh deployment's first cold start
		// fail its warm pass and publish nothing.
		{name: "403 is how an absent key reads without s3:ListBucket", status: http.StatusForbidden, wantExists: false},
		{name: "500 is a real error", status: http.StatusInternalServerError, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := s3Store(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodHead {
					t.Errorf("method = %s, want HEAD", r.Method)
				}
				w.WriteHeader(tc.status)
			})

			exists, err := store.objectExists(context.Background(), "assets-xyz", "ocel/bytecode/my-app/node24-arm64.tar.gz")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("objectExists() = %v, nil, want an error", exists)
				}
				return
			}
			if err != nil {
				t.Fatalf("objectExists() unexpected error: %v", err)
			}
			if exists != tc.wantExists {
				t.Errorf("objectExists() = %v, want %v", exists, tc.wantExists)
			}
		})
	}
}

func TestS3BytecodeStore_PutObject(t *testing.T) {
	var gotPath string
	var gotBody []byte
	store, _ := s3Store(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})

	if err := store.putObject(context.Background(), "assets-xyz", "ocel/bytecode/my-app/node24-arm64.tar.gz", []byte("archive bytes")); err != nil {
		t.Fatalf("putObject: %v", err)
	}
	if want := "/assets-xyz/ocel/bytecode/my-app/node24-arm64.tar.gz"; gotPath != want {
		t.Errorf("put path = %q, want %q", gotPath, want)
	}
	if string(gotBody) != "archive bytes" {
		t.Errorf("put body = %q, want %q", gotBody, "archive bytes")
	}
}

func TestS3BytecodeStore_PutObjectReportsAFailure(t *testing.T) {
	store, _ := s3Store(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	if err := store.putObject(context.Background(), "assets-xyz", "some/key", []byte("x")); err == nil {
		t.Error("putObject() error = nil, want the failure reported")
	}
}

// A NoSuchKey body is the answer "nothing to rehydrate yet", not a failure.
// Getting this wrong reads a routine cold start as a fault on every deploy's
// first instance, which is the miss this store exists to tell apart.
func TestS3BytecodeStore_GetObject(t *testing.T) {
	t.Run("200 returns the body and content length", func(t *testing.T) {
		store, _ := s3Store(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("method = %s, want GET", r.Method)
			}
			w.Header().Set("Content-Length", "13")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("archive bytes"))
		})

		body, size, err := store.getObject(context.Background(), "assets-xyz", "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz")
		if err != nil {
			t.Fatalf("getObject: %v", err)
		}
		defer body.Close()
		if size != 13 {
			t.Errorf("size = %d, want 13", size)
		}
		got, err := io.ReadAll(body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(got) != "archive bytes" {
			t.Errorf("body = %q, want %q", got, "archive bytes")
		}
	})

	t.Run("a NoSuchKey error body is a miss, not an error", func(t *testing.T) {
		store, _ := s3Store(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`))
		})

		_, _, err := store.getObject(context.Background(), "assets-xyz", "missing/key")
		if !errors.Is(err, errBytecodeCacheMiss) {
			t.Errorf("getObject() error = %v, want errBytecodeCacheMiss", err)
		}
	})

	t.Run("403 is a real error, not an absence", func(t *testing.T) {
		store, _ := s3Store(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`))
		})

		_, _, err := store.getObject(context.Background(), "assets-xyz", "some/key")
		if err == nil {
			t.Fatal("getObject() error = nil, want the failure reported")
		}
		if errors.Is(err, errBytecodeCacheMiss) {
			t.Error("getObject() reported a miss for an access failure")
		}
	})
}

// tarEntry is a hand-built archive member, for tests that need to plant an
// entry buildBytecodeArchive would never produce — an absolute path, a
// traversal, a symlink, an oversized declared size — to prove untarInto
// rejects it rather than trusting the writer.
type tarEntry struct {
	name     string
	typeflag byte
	content  []byte
	linkname string
}

func buildArchive(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Size:     int64(len(e.content)),
			Mode:     0o644,
			Linkname: e.linkname,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header for %q: %v", e.name, err)
		}
		if len(e.content) > 0 {
			if _, err := tw.Write(e.content); err != nil {
				t.Fatalf("write content for %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

// ustarHeader hand-builds a raw 512-byte USTAR header block for a name
// archive/tar's own Writer refuses to emit (a trailing-slash name, in
// particular — see ustarArchive). A byte stream a hostile uploader controls
// is under no obligation to come from that writer, so the regression for
// "an entry that resolves to dir itself" has to construct bytes the writer
// cannot produce, or it only ever proves the safe case.
func ustarHeader(name string, typeflag byte, size int64) []byte {
	b := make([]byte, 512)
	copy(b[0:100], name)
	copy(b[100:108], fmt.Appendf(nil, "%07o\x00", 0o644))
	copy(b[108:116], fmt.Appendf(nil, "%07o\x00", 0))
	copy(b[116:124], fmt.Appendf(nil, "%07o\x00", 0))
	copy(b[124:136], fmt.Appendf(nil, "%011o\x00", size))
	copy(b[136:148], fmt.Appendf(nil, "%011o\x00", 0))
	for i := 148; i < 156; i++ {
		b[i] = ' ' // checksum field reads as spaces while the checksum itself is computed
	}
	b[156] = typeflag
	copy(b[257:263], "ustar\x00")
	copy(b[263:265], "00")

	var sum int
	for _, c := range b {
		sum += int(c)
	}
	copy(b[148:156], fmt.Appendf(nil, "%06o\x00 ", sum))
	return b
}

// ustarArchive gzip-compresses a hand-built raw tar stream: one entry with
// the given name, typeflag and content, followed by the two zero blocks that
// mark end of archive — bypassing archive/tar.Writer entirely, which is the
// point: it rejects a trailing-slash name outright, but a name of "" or "."
// it will happily emit and this test suite exercises those through
// buildArchive already. Only "./" needs this hand-built path.
func ustarArchive(t *testing.T, name string, typeflag byte, content []byte) []byte {
	t.Helper()
	var raw bytes.Buffer
	raw.Write(ustarHeader(name, typeflag, int64(len(content))))
	raw.Write(content)
	if pad := (512 - len(content)%512) % 512; pad > 0 {
		raw.Write(make([]byte, pad))
	}
	raw.Write(make([]byte, 1024)) // two zero blocks mark end of archive

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw.Bytes()); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// TestUntarInto_RejectsAnEntryThatResolvesToTheCacheDirectoryItself proves an
// entry named "", "." or "./" — all of which filepath.Clean collapses to "."
// — is rejected rather than written to dir itself. Without this check,
// os.OpenFile(dir, O_TRUNC, ...) replaces the just-wiped cache directory with
// a regular file, and untarInto reports success: node would then find
// NODE_COMPILE_CACHE pointing at a file instead of a directory.
func TestUntarInto_RejectsAnEntryThatResolvesToTheCacheDirectoryItself(t *testing.T) {
	for _, name := range []string{"", ".", "./"} {
		t.Run(fmt.Sprintf("name=%q", name), func(t *testing.T) {
			var archive []byte
			if name == "./" {
				// archive/tar.Writer refuses a trailing slash outright, so this
				// one case has to be hand-built to prove the reader still
				// defends against it.
				archive = ustarArchive(t, name, tar.TypeReg, []byte("abc"))
			} else {
				archive = buildArchive(t, []tarEntry{{name: name, typeflag: tar.TypeReg, content: []byte("abc")}})
			}

			dest := filepath.Join(t.TempDir(), "cache")
			if _, err := untarGzipInto(context.Background(), bytes.NewReader(archive), dest, bytecodeCacheCeiling); err == nil {
				t.Fatalf("untarGzipInto(name=%q) error = nil, want the entry rejected", name)
			}
			if info, err := os.Stat(dest); err == nil {
				t.Fatalf("dest = %v after a rejected entry, want it never created", info.Mode())
			}
		})
	}
}

// zeroReader streams zero bytes forever, so a test can declare a tar entry of
// any size without holding that many bytes in memory: gzip compresses a run
// of zeros to almost nothing, which is what lets the ceiling-during-streaming
// test use a realistic 64MiB bound without costing real time or memory.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// TestUntarInto_RoundTrip proves untarInto is buildBytecodeArchive's inverse:
// an archive built from a node-shaped compile cache directory (one version-hash
// subdirectory, arbitrary file names) extracts back to the same tree.
func TestUntarInto_RoundTrip(t *testing.T) {
	src := t.TempDir()
	versionDir := filepath.Join(src, "v24.3.1-x64-abc123-1000")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(src, "index.bin"):       "top level",
		filepath.Join(versionDir, "aabbccdd"): "nested one",
		filepath.Join(versionDir, "11223344"): "nested two",
	}
	for path, contents := range files {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	archive, err := buildBytecodeArchive(context.Background(), src)
	if err != nil {
		t.Fatalf("buildBytecodeArchive: %v", err)
	}

	dest := t.TempDir()
	n, err := untarGzipInto(context.Background(), bytes.NewReader(archive), dest, bytecodeCacheCeiling)
	if err != nil {
		t.Fatalf("untarGzipInto: %v", err)
	}
	var want int64
	for _, contents := range files {
		want += int64(len(contents)) + tarEntryOverhead
	}
	if n != want {
		t.Errorf("untarGzipInto() = %d bytes, want %d", n, want)
	}
	for path, contents := range files {
		rel, err := filepath.Rel(src, path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil {
			t.Fatalf("read back %s: %v", rel, err)
		}
		if string(got) != contents {
			t.Errorf("%s = %q, want %q", rel, got, contents)
		}
	}
}

// TestUntarInto_ClampsExtractedFilePermissions proves an archive's declared
// mode never reaches disk: mode 0000 is reachable from a corrupt or hostile
// archive, and honouring it would leave a cache file the runtime user can't
// even read back — a silent, self-inflicted degradation the upload leg would
// never itself produce, since it only ever writes something in the 0644
// family.
func TestUntarInto_ClampsExtractedFilePermissions(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	content := []byte("compiled")
	if err := tw.WriteHeader(&tar.Header{
		Name:     "unreadable.blob",
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
		Mode:     0o000,
	}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}

	dest := t.TempDir()
	if _, err := untarGzipInto(context.Background(), bytes.NewReader(buf.Bytes()), dest, bytecodeCacheCeiling); err != nil {
		t.Fatalf("untarGzipInto: %v", err)
	}

	info, err := os.Stat(filepath.Join(dest, "unreadable.blob"))
	if err != nil {
		t.Fatalf("stat extracted file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("extracted file mode = %o, want 0644 regardless of the archive's declared mode", got)
	}
}

// TestUntarInto_AbortsPastTheCeiling proves the running total is checked
// against the caller's ceiling as entries stream in, not just once at the end,
// so a hostile or corrupt archive cannot fill the sandbox's disk before the
// check catches up.
func TestUntarInto_AbortsPastTheCeiling(t *testing.T) {
	archive := buildArchive(t, []tarEntry{
		{name: "a.bin", typeflag: tar.TypeReg, content: bytes.Repeat([]byte("x"), 100)},
		{name: "b.bin", typeflag: tar.TypeReg, content: bytes.Repeat([]byte("y"), 100)},
	})

	if _, err := untarGzipInto(context.Background(), bytes.NewReader(archive), t.TempDir(), 150); err == nil {
		t.Fatal("untarGzipInto() error = nil, want the ceiling enforced")
	}
}

// TestUntarInto_BoundsEntryCountEvenWithZeroSizedEntries proves the ceiling
// stops a stream of empty entries, not just a stream of large ones: without
// charging tarEntryOverhead per entry, hdr.Size never advances the running
// total for a zero-byte file, so an archive of empty entries would never
// trip the ceiling and would keep creating real inodes under dest until the
// disk or the context gave out.
func TestUntarInto_BoundsEntryCountEvenWithZeroSizedEntries(t *testing.T) {
	entries := make([]tarEntry, 1000)
	for i := range entries {
		entries[i] = tarEntry{name: fmt.Sprintf("%04d.bin", i), typeflag: tar.TypeReg}
	}
	archive := buildArchive(t, entries)

	const ceilingEntries = 10
	ceiling := int64(ceilingEntries * tarEntryOverhead)
	dest := t.TempDir()

	if _, err := untarGzipInto(context.Background(), bytes.NewReader(archive), dest, ceiling); err == nil {
		t.Fatal("untarGzipInto() error = nil, want the ceiling enforced against entry count alone")
	}

	created, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if len(created) != ceilingEntries {
		t.Errorf("created %d entries under a %d-entry ceiling, want exactly %d", len(created), ceilingEntries, ceilingEntries)
	}
}

// rehydrateFixture builds a fake store whose GET returns the given archive
// bytes with a declared size, for tests exercising rehydrateCompileCache
// against a hand-built or hand-corrupted archive.
func rehydrateFixture(archive []byte) *fakeBytecodeStore {
	return &fakeBytecodeStore{getBody: io.NopCloser(bytes.NewReader(archive)), getSize: int64(len(archive))}
}

// assertNoCacheDir fails the test if dir exists, which is how every failed
// rehydration attempt must leave it: a half-populated cache directory is
// worse than none, since node would trust whatever is there.
func assertNoCacheDir(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cache dir %s exists after a failed rehydration, want it absent", dir)
	}
}

func TestRehydrateCompileCache_RoundTrip(t *testing.T) {
	src := t.TempDir()
	versionDir := filepath.Join(src, "v24.3.1-x64-abc123-1000")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "index.bin"), []byte("top"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionDir, "aabbccdd"), []byte("nested"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive, err := buildBytecodeArchive(context.Background(), src)
	if err != nil {
		t.Fatalf("buildBytecodeArchive: %v", err)
	}
	store := rehydrateFixture(archive)
	dest := filepath.Join(t.TempDir(), "cache")

	n, ok := rehydrateCompileCache(context.Background(), store, "assets-xyz", "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz", dest)
	if !ok {
		t.Fatal("rehydrateCompileCache() ok = false, want a successful round trip")
	}
	if want := int64(len("top")+len("nested")) + 2*tarEntryOverhead; n != want {
		t.Errorf("rehydrateCompileCache() = %d bytes, want %d", n, want)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "index.bin")); err != nil || string(got) != "top" {
		t.Errorf("index.bin = %q, %v, want %q, nil", got, err, "top")
	}
	if got, err := os.ReadFile(filepath.Join(dest, "v24.3.1-x64-abc123-1000", "aabbccdd")); err != nil || string(got) != "nested" {
		t.Errorf("nested file = %q, %v, want %q, nil", got, err, "nested")
	}
	if len(store.gets) != 1 || store.gets[0] != "assets-xyz/ocel/bytecode/my-app/node24.3.1-arm64.tar.gz" {
		t.Errorf("gets = %v, want a single get of the composed key", store.gets)
	}
}

// A miss is the expected state on a deployment's first cold start; it must
// not touch the target directory at all, and must not read as a fault.
func TestRehydrateCompileCache_MissTouchesNothing(t *testing.T) {
	store := &fakeBytecodeStore{getErr: errBytecodeCacheMiss}
	dest := filepath.Join(t.TempDir(), "cache")

	n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
	if ok || n != 0 {
		t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) on a miss", n, ok)
	}
	assertNoCacheDir(t, dest)
}

// captureStderr redirects the package-level os.Stderr for the duration of fn
// and returns what was written to it, so a test can assert on which of two
// log lines a function chose rather than only on its return value. Tests in
// this file don't run in parallel, so swapping the global is safe here.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = orig
	w.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return string(out)
}

// A miss and a failure both return (0, false), but the brief requires them to
// log differently: a miss is the expected state on a deployment's first cold
// start and must not read as a fault. This is the test that actually pins
// that down — the (0, false) assertions elsewhere in this file would stay
// green even if the two Fprintf calls in rehydrateCompileCache were swapped.
func TestRehydrateCompileCache_LogsAMissDifferentlyFromAFailure(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "cache")

	missOut := captureStderr(t, func() {
		store := &fakeBytecodeStore{getErr: errBytecodeCacheMiss}
		rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
	})
	if !strings.Contains(missOut, "nothing to rehydrate") {
		t.Errorf("miss log = %q, want it to name a miss", missOut)
	}
	if strings.Contains(missOut, "could not") {
		t.Errorf("miss log = %q, want no failure wording for the expected first-cold-start case", missOut)
	}

	failOut := captureStderr(t, func() {
		store := &fakeBytecodeStore{getErr: errors.New("access denied")}
		rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
	})
	if !strings.Contains(failOut, "could not fetch") {
		t.Errorf("failure log = %q, want it to name a fetch failure", failOut)
	}
	if strings.Contains(failOut, "nothing to rehydrate") {
		t.Errorf("failure log = %q, want it not to read as the expected miss case", failOut)
	}

	if missOut == failOut {
		t.Error("miss and failure produced the identical log line")
	}
}

func TestRehydrateCompileCache_NonGzipBodyLeavesNoDirectory(t *testing.T) {
	body := []byte("not a gzip stream")
	store := &fakeBytecodeStore{getBody: io.NopCloser(bytes.NewReader(body)), getSize: int64(len(body))}
	dest := filepath.Join(t.TempDir(), "cache")

	n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
	if ok || n != 0 {
		t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) on a corrupt body", n, ok)
	}
	assertNoCacheDir(t, dest)
}

// A traversal entry must be rejected before anything is written, and nothing
// from it may land outside the target directory — checked here by looking in
// the temp dir's parent, not just at whether the extraction reported success.
func TestRehydrateCompileCache_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "cache")
	archive := buildArchive(t, []tarEntry{{name: "../escape", typeflag: tar.TypeReg, content: []byte("hax")}})
	store := rehydrateFixture(archive)

	n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
	if ok || n != 0 {
		t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) for a traversal entry", n, ok)
	}
	assertNoCacheDir(t, dest)
	if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
		t.Error("a traversal entry escaped into the temp dir's parent")
	}
}

func TestRehydrateCompileCache_RejectsAbsolutePath(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "cache")
	archive := buildArchive(t, []tarEntry{{name: "/etc/passwd", typeflag: tar.TypeReg, content: []byte("hax")}})
	store := rehydrateFixture(archive)

	n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
	if ok || n != 0 {
		t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) for an absolute path", n, ok)
	}
	assertNoCacheDir(t, dest)
}

func TestRehydrateCompileCache_RejectsSymlink(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "cache")
	archive := buildArchive(t, []tarEntry{{name: "link.bin", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}})
	store := rehydrateFixture(archive)

	n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
	if ok || n != 0 {
		t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) for a symlink entry", n, ok)
	}
	assertNoCacheDir(t, dest)
}

// An entry that resolves to dir itself must not be reported as a successful
// rehydration: os.OpenFile(dir, O_TRUNC, ...) would otherwise replace the
// just-wiped cache directory with a regular file at dir's own path, which
// rehydrateCompileCache's ok=true return would then vouch for. This is the
// full-attempt version of TestUntarInto_RejectsAnEntryThatResolvesToTheCacheDirectoryItself,
// checking the same attack through the caller that decides success and
// disables the upload leg on a hit.
func TestRehydrateCompileCache_RejectsAnEntryThatResolvesToTheCacheDirectoryItself(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "cache")
	// archive/tar.Writer refuses a trailing-slash name outright, so only the
	// hand-built path can exercise it; "" and "." are covered directly against
	// untarInto above.
	archive := ustarArchive(t, "./", tar.TypeReg, []byte("abc"))
	store := rehydrateFixture(archive)

	n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
	if ok || n != 0 {
		t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) for an entry targeting dir itself", n, ok)
	}
	if info, err := os.Stat(dest); err == nil {
		t.Fatalf("dest = %v, want it absent rather than replaced by a file", info.Mode())
	}
}

// A ContentLength over the ceiling must bail before a single byte of the body
// is read — proven here by a body that fails the test if Read is ever called,
// not just by asserting the outcome.
type poisonReader struct{ t *testing.T }

func (p poisonReader) Read([]byte) (int, error) {
	p.t.Fatal("read from a body that should have been rejected on ContentLength alone")
	return 0, io.EOF
}

func TestRehydrateCompileCache_BailsOnContentLengthBeforeAnyRead(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "cache")
	store := &fakeBytecodeStore{
		getBody: io.NopCloser(poisonReader{t}),
		getSize: bytecodeCacheCeiling + 1,
	}

	n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
	if ok || n != 0 {
		t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) over the ceiling", n, ok)
	}
	assertNoCacheDir(t, dest)
}

// The ContentLength precheck only catches an oversized compressed archive; a
// highly compressible one can report a tiny ContentLength while decompressing
// past the ceiling, so untarInto's running-total check is what actually stops
// it. zeroReader keeps this fast and cheap despite the size being realistic.
func TestRehydrateCompileCache_AbortsWhenStreamedContentExceedsTheCeiling(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	size := int64(bytecodeCacheCeiling) + 1<<20
	if err := tw.WriteHeader(&tar.Header{Name: "huge.bin", Typeflag: tar.TypeReg, Size: size, Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(tw, zeroReader{}, size); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buf.Bytes()

	dest := filepath.Join(t.TempDir(), "cache")
	store := rehydrateFixture(archive)
	if store.getSize >= bytecodeCacheCeiling {
		t.Fatalf("test archive's compressed size %d did not stay under the ceiling", store.getSize)
	}

	n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
	if ok || n != 0 {
		t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) once streamed content passes the ceiling", n, ok)
	}
	assertNoCacheDir(t, dest)
}

func TestRehydrateCompileCache_WipesStaleContentBeforeExtracting(t *testing.T) {
	dest := t.TempDir()
	stale := filepath.Join(dest, "stale.bin")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	archive := buildArchive(t, []tarEntry{{name: "fresh.bin", typeflag: tar.TypeReg, content: []byte("new")}})
	store := rehydrateFixture(archive)

	n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
	if want := int64(3 + tarEntryOverhead); !ok || n != want {
		t.Fatalf("rehydrateCompileCache() = (%d, %v), want (%d, true)", n, ok, want)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale content survived rehydration")
	}
	if got, err := os.ReadFile(filepath.Join(dest, "fresh.bin")); err != nil || string(got) != "new" {
		t.Errorf("fresh.bin = %q, %v, want %q, nil", got, err, "new")
	}
}

// A cancelled context has to stop the extraction mid-stream, not just fail to
// start one: the body is closed to interrupt whatever Read the goroutine is
// blocked in, and the caller waits for that goroutine before cleaning up, so
// no write can race the RemoveAll that follows.
func TestRehydrateCompileCache_CancelledContextAbortsAndCleansUp(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "cache")
	pr, pw := io.Pipe()
	go func() {
		gz := gzip.NewWriter(pw)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(&tar.Header{Name: "a.bin", Typeflag: tar.TypeReg, Size: 3, Mode: 0o644})
		tw.Write([]byte("abc"))
		gz.Flush()
		// tw, gz and pw are deliberately left open: the reader blocks waiting
		// for the archive's end, which never arrives until the pipe is closed,
		// giving the cancellation something to interrupt.
	}()

	store := &fakeBytecodeStore{getBody: pr, getSize: 1 << 20}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	n, ok := rehydrateCompileCache(ctx, store, "bucket", "key", dest)
	elapsed := time.Since(start)

	if ok || n != 0 {
		t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) once cancelled", n, ok)
	}
	if elapsed > time.Second {
		t.Errorf("took %s, want the extraction interrupted at the context deadline", elapsed)
	}
	assertNoCacheDir(t, dest)
}

// A miss logs through rehydrateCompileCache itself, so only the hit needs a
// line here — and that line is what a later grep against CloudWatch will
// look for, so this pins its exact shape: the key, the bytes restored and
// the elapsed milliseconds.
func TestRehydrateBytecodeCache_LogsTheHitWithKeyBytesAndElapsedMS(t *testing.T) {
	archive, err := buildBytecodeArchive(context.Background(), cacheDirWith(t, "compiled bytes"))
	if err != nil {
		t.Fatalf("buildBytecodeArchive: %v", err)
	}
	r := &bytecodeResolution{
		store:  rehydrateFixture(archive),
		bucket: "assets-xyz",
		key:    "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz",
	}
	dest := filepath.Join(t.TempDir(), "cache")

	var hit bool
	out := captureStderr(t, func() {
		hit = rehydrateBytecodeCache(context.Background(), r, dest)
	})
	if !hit {
		t.Fatal("rehydrateBytecodeCache() = false, want a hit")
	}
	if !strings.Contains(out, "ocel: rehydrated compile cache from ocel/bytecode/my-app/node24.3.1-arm64.tar.gz:") {
		t.Errorf("log = %q, want it to name the key", out)
	}
	if !strings.Contains(out, "bytes in") || !strings.Contains(out, "ms") {
		t.Errorf("log = %q, want bytes and elapsed ms", out)
	}
}

func TestRehydrateBytecodeCache_MissLogsNothingOfItsOwn(t *testing.T) {
	r := &bytecodeResolution{store: &fakeBytecodeStore{getErr: errBytecodeCacheMiss}, bucket: "b", key: "k"}
	dest := filepath.Join(t.TempDir(), "cache")

	var hit bool
	out := captureStderr(t, func() {
		hit = rehydrateBytecodeCache(context.Background(), r, dest)
	})
	if hit {
		t.Fatal("rehydrateBytecodeCache() = true, want a miss")
	}
	// rehydrateCompileCache already logged the miss with the key and the
	// reason; rehydrateBytecodeCache must not log a second, redundant line.
	if strings.Count(out, "\n") != 1 {
		t.Errorf("log = %q, want exactly the one line rehydrateCompileCache already wrote", out)
	}
}

// blockingGetStore's getObject hangs until its context ends. Without the
// context.WithTimeout that rehydrateBytecodeCache applies around
// rehydrateCompileCache, this store's GET would still be blocked when this
// test's own failsafe deadline arrived — so a passing test here is what
// proves bytecodeRehydrateBudget is actually wired in, not merely declared.
type blockingGetStore struct{}

func (blockingGetStore) objectExists(context.Context, string, string) (bool, error) {
	return false, nil
}
func (blockingGetStore) putObject(context.Context, string, string, []byte) error { return nil }
func (blockingGetStore) getObject(ctx context.Context, _, _ string) (io.ReadCloser, int64, error) {
	<-ctx.Done()
	return nil, 0, ctx.Err()
}

func TestRehydrateBytecodeCache_AppliesItsOwnBudget(t *testing.T) {
	r := &bytecodeResolution{store: blockingGetStore{}, bucket: "b", key: "k"}
	dest := filepath.Join(t.TempDir(), "cache")

	done := make(chan bool, 1)
	go func() { done <- rehydrateBytecodeCache(context.Background(), r, dest) }()

	select {
	case hit := <-done:
		if hit {
			t.Error("rehydrateBytecodeCache() = true, want false once the store hangs past its budget")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("rehydrateBytecodeCache did not return within 3s, want bytecodeRehydrateBudget enforced")
	}
}

// TestBytecodeRehydrate_TargetsTheDirCompileCacheEnvDeclares pins the property
// that makes bytecodeRehydrate (main.go) safe: nothing here checks that the
// archive lands where NODE_COMPILE_CACHE actually points except that both
// read the same compileCacheDir constant. This proves it by construction
// rather than by name — it reads the directory out of compileCacheEnv's own
// output and asserts bytecodeRehydrate wrote there, so the test still catches
// a future edit that gave either side its own literal.
func TestBytecodeRehydrate_TargetsTheDirCompileCacheEnvDeclares(t *testing.T) {
	t.Setenv(bytecodePrefixEnvVar, "ocel")
	env := compileCacheEnv()
	if len(env) != 1 || !strings.HasPrefix(env[0], "NODE_COMPILE_CACHE=") {
		t.Fatalf("compileCacheEnv() = %v, want exactly one NODE_COMPILE_CACHE entry", env)
	}
	dir := strings.TrimPrefix(env[0], "NODE_COMPILE_CACHE=")
	t.Cleanup(func() { os.RemoveAll(dir) })

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "cached.blob"), []byte("compiled"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive, err := buildBytecodeArchive(context.Background(), src)
	if err != nil {
		t.Fatalf("buildBytecodeArchive: %v", err)
	}
	r := &bytecodeResolution{store: rehydrateFixture(archive), bucket: "b", key: "k"}

	if !bytecodeRehydrate(context.Background(), r) {
		t.Fatal("bytecodeRehydrate() = false, want a hit")
	}
	got, err := os.ReadFile(filepath.Join(dir, "cached.blob"))
	if err != nil || string(got) != "compiled" {
		t.Errorf("file at compileCacheEnv's dir = %q, %v, want %q, nil: bytecodeRehydrate wrote somewhere else", got, err, "compiled")
	}
}

// The upload leg needs the mirror of what the test above pins for the read
// leg, and for the same reason: the archive is rooted at NODE_COMPILE_CACHE
// itself, so a resolution that handed the upload anything else — node's own
// reported subdirectory, most temptingly — publishes an archive the read leg
// restores one level too high.
func TestBytecodeUpload_IsRootedAtTheDirCompileCacheEnvDeclares(t *testing.T) {
	t.Setenv(bytecodePrefixEnvVar, "ocel")
	env := compileCacheEnv()
	if len(env) != 1 || !strings.HasPrefix(env[0], "NODE_COMPILE_CACHE=") {
		t.Fatalf("compileCacheEnv() = %v, want exactly one NODE_COMPILE_CACHE entry", env)
	}
	dir := strings.TrimPrefix(env[0], "NODE_COMPILE_CACHE=")

	r := &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "k"}
	u := r.upload(func(context.Context) (compileCacheFlushedPayload, bool) {
		return compileCacheFlushedPayload{}, false
	})
	if u.root != dir {
		t.Errorf("upload root = %q, want %q: the two legs disagree on where the cache lives", u.root, dir)
	}
}

// fakeSpawn records the budget bringUp handed it and returns an empty
// Membrane, standing in for startNode the same way live_test.go's spawns do.
func fakeSpawn(gotBudget *time.Duration) spawner {
	return func(_ []string, budget time.Duration, onControl func(io.Writer), _ <-chan struct{}) (*Membrane, error) {
		*gotBudget = budget
		if onControl != nil {
			onControl(&sink{})
		}
		return &Membrane{}, nil
	}
}

// TestBringUpWithBytecode_RehydrationsCostIsCarvedOutOfStartupBudget is the
// property the brief calls out by name: rehydration runs before bringUp's
// budget argument is computed, so whatever it spends is already reflected in
// time.Since(start) by the time bringUp sees it — carved out of
// startupBudget, not added on top of it.
func TestBringUpWithBytecode_RehydrationsCostIsCarvedOutOfStartupBudget(t *testing.T) {
	const rehydrateCost = 200 * time.Millisecond

	l := newLiveValues(resolves(nil), nil, nil)
	bytecodeReady := make(chan *bytecodeResolution, 1)
	bytecodeReady <- &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "k"}

	rehydrate := func(context.Context, *bytecodeResolution) bool {
		time.Sleep(rehydrateCost)
		return false
	}

	var gotBudget time.Duration
	start := time.Now()
	membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, start, bytecodeReady, neverEmbedded, rehydrate)
	if err != nil {
		t.Fatalf("bringUpWithBytecode: %v", err)
	}
	if membrane.bytecode == nil {
		t.Fatal("bytecode = nil, want the upload leg attached on a miss")
	}

	// The budget bringUp received must already be short by roughly what
	// rehydration cost — not the full startupBudget, which is what "added on
	// top" would look like.
	if gotBudget > startupBudget-rehydrateCost/2 {
		t.Errorf("budget handed to bringUp = %s, want it reduced by rehydration's %s cost", gotBudget, rehydrateCost)
	}
}

// TestBringUpWithBytecode_FloorsTheSpawnBudget proves the pathological "slow
// miss" — pre-spawn work eating past startupBudget with nothing to show for
// it — hands bringUp minSpawnBudget rather than a non-positive duration. A
// non-positive budget is what turns into awaitReady's time.After(negative)
// firing immediately, so this is what keeps that boot from failing spuriously.
func TestBringUpWithBytecode_FloorsTheSpawnBudget(t *testing.T) {
	l := newLiveValues(resolves(nil), nil, nil)
	bytecodeReady := make(chan *bytecodeResolution, 1)
	bytecodeReady <- &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "k"}
	rehydrate := func(context.Context, *bytecodeResolution) bool { return false }

	// start set so far in the past that startupBudget - time.Since(start) is
	// already deep in negative territory before bringUp even runs.
	start := time.Now().Add(-2 * startupBudget)

	var gotBudget time.Duration
	if _, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, start, bytecodeReady, neverEmbedded, rehydrate); err != nil {
		t.Fatalf("bringUpWithBytecode: %v", err)
	}
	if gotBudget != minSpawnBudget {
		t.Errorf("budget handed to bringUp = %s, want the floor of %s", gotBudget, minSpawnBudget)
	}
}

// TestBringUpWithBytecode_NormalCarveOutIsUnaffectedByTheFloor proves the
// floor only ever engages in the pathological case: a start recent enough
// that startupBudget - time.Since(start) is comfortably above minSpawnBudget
// must reach bringUp unchanged, not clamped.
func TestBringUpWithBytecode_NormalCarveOutIsUnaffectedByTheFloor(t *testing.T) {
	l := newLiveValues(resolves(nil), nil, nil)
	bytecodeReady := make(chan *bytecodeResolution, 1)
	bytecodeReady <- &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "k"}
	rehydrate := func(context.Context, *bytecodeResolution) bool { return false }

	start := time.Now()
	var gotBudget time.Duration
	if _, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, start, bytecodeReady, neverEmbedded, rehydrate); err != nil {
		t.Fatalf("bringUpWithBytecode: %v", err)
	}
	if gotBudget <= minSpawnBudget {
		t.Errorf("budget handed to bringUp = %s, want it well above the floor for a start this recent", gotBudget)
	}
	if gotBudget > startupBudget {
		t.Errorf("budget handed to bringUp = %s, want it at most startupBudget", gotBudget)
	}
}

// A rehydrate hit disables the whole upload leg, not merely its PUT: the
// object is proven to exist, so nothing membrane.bytecode would do could
// ever matter, and it is left nil rather than wired up and never used.
func TestBringUpWithBytecode_AHitDisablesTheUploadLeg(t *testing.T) {
	l := newLiveValues(resolves(nil), nil, nil)
	bytecodeReady := make(chan *bytecodeResolution, 1)
	bytecodeReady <- &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "k"}

	rehydrateCalls := 0
	rehydrate := func(context.Context, *bytecodeResolution) bool {
		rehydrateCalls++
		return true
	}

	var gotBudget time.Duration
	membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, neverEmbedded, rehydrate)
	if err != nil {
		t.Fatalf("bringUpWithBytecode: %v", err)
	}
	if rehydrateCalls != 1 {
		t.Errorf("rehydrate calls = %d, want 1", rehydrateCalls)
	}
	if membrane.bytecode != nil {
		t.Error("bytecode != nil, want the upload leg disabled after a rehydrate hit")
	}
}

// A miss attaches the upload leg, wired to the same key rehydration read
// from — the two legs sharing bytecode.upload's composition is what this
// checks, not just that some upload got attached.
func TestBringUpWithBytecode_AMissAttachesTheUploadLegWithTheSameKey(t *testing.T) {
	l := newLiveValues(resolves(nil), nil, nil)
	bytecodeReady := make(chan *bytecodeResolution, 1)
	r := &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "assets-xyz", key: "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz"}
	bytecodeReady <- r

	rehydrate := func(context.Context, *bytecodeResolution) bool { return false }

	var gotBudget time.Duration
	membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, neverEmbedded, rehydrate)
	if err != nil {
		t.Fatalf("bringUpWithBytecode: %v", err)
	}
	if membrane.bytecode == nil {
		t.Fatal("bytecode = nil, want the upload leg attached on a miss")
	}
	if membrane.bytecode.bucket != r.bucket || membrane.bytecode.key != r.key {
		t.Errorf("upload bucket/key = %s/%s, want the resolution's %s/%s",
			membrane.bytecode.bucket, membrane.bytecode.key, r.bucket, r.key)
	}
}

// An unconfigured deployment's nil resolution never reaches rehydrate at
// all, and attaches no upload leg either.
func TestBringUpWithBytecode_NilResolutionSkipsBothLegs(t *testing.T) {
	l := newLiveValues(resolves(nil), nil, nil)
	bytecodeReady := make(chan *bytecodeResolution, 1)
	bytecodeReady <- nil

	rehydrateCalls := 0
	rehydrate := func(context.Context, *bytecodeResolution) bool {
		rehydrateCalls++
		return false
	}

	var gotBudget time.Duration
	membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, neverEmbedded, rehydrate)
	if err != nil {
		t.Fatalf("bringUpWithBytecode: %v", err)
	}
	if rehydrateCalls != 0 {
		t.Errorf("rehydrate calls = %d, want 0 for an unconfigured deployment", rehydrateCalls)
	}
	if membrane.bytecode != nil {
		t.Error("bytecode != nil, want no upload leg for an unconfigured deployment")
	}
}

// The join must not be a bare receive: a resolution goroutine that never
// answers — wedged exec, stalled config load, or simply one that ignored its
// own context entirely — must still let bringUpWithBytecode reach the spawn,
// and must not drive the budget it hands to bringUp negative. bytecodeReady
// is unbuffered and never written to here, standing in for a goroutine that
// hangs forever.
func TestBringUpWithBytecode_AResolutionThatNeverArrivesDoesNotBlockTheSpawn(t *testing.T) {
	l := newLiveValues(resolves(nil), nil, nil)
	bytecodeReady := make(chan *bytecodeResolution)

	rehydrateCalls := 0
	rehydrate := func(context.Context, *bytecodeResolution) bool {
		rehydrateCalls++
		return false
	}

	var gotBudget time.Duration
	start := time.Now()
	done := make(chan struct{})
	var membrane *Membrane
	var err error
	go func() {
		membrane, err = bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, start, bytecodeReady, neverEmbedded, rehydrate)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(startupBudget):
		t.Fatal("bringUpWithBytecode never returned; a resolution that never arrived blocked the spawn")
	}
	if err != nil {
		t.Fatalf("bringUpWithBytecode: %v", err)
	}
	if rehydrateCalls != 0 {
		t.Errorf("rehydrate calls = %d, want 0 when the resolution never arrived", rehydrateCalls)
	}
	if membrane.bytecode != nil {
		t.Error("bytecode != nil, want no upload leg when the resolution never arrived")
	}
	if gotBudget <= 0 {
		t.Errorf("budget handed to bringUp = %s, want a positive budget rather than one driven negative", gotBudget)
	}
	if gotBudget >= startupBudget {
		t.Errorf("budget handed to bringUp = %s, want it reduced by the time spent waiting on the resolution", gotBudget)
	}
}

// buildTar is buildArchive without the gzip layer, standing in for what the
// deploy bakes into the deployment package: a plain tar the zip container
// deflated, so the membrane never gunzips on the init path.
func buildTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Size:     int64(len(e.content)),
			Mode:     0o644,
			Linkname: e.linkname,
		}); err != nil {
			t.Fatalf("write header for %q: %v", e.name, err)
		}
		if len(e.content) > 0 {
			if _, err := tw.Write(e.content); err != nil {
				t.Fatalf("write content for %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

// The embedded path is derived from the key the resolution already composed,
// never recomposed from a version and an arch of its own: the two would drift
// on a runtime bump and the membrane would read a stale cache under a fresh
// key. Composing the expectation through bytecodeCacheKey is the point — it
// fails if either side ever grows its own spelling.
func TestEmbeddedBytecodePath_FollowsTheResolutionsKey(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{bytecodeCacheKey("ocel", "my-app", "24.3.1", "arm64"), "/var/task/.ocel/bytecode/node24.3.1-arm64.tar"},
		{bytecodeCacheKey("stg/deploy", "other-fn", "20.11.0", "amd64"), "/var/task/.ocel/bytecode/node20.11.0-x86_64.tar"},
	}
	for _, c := range cases {
		if got := embeddedBytecodePath(c.key); got != c.want {
			t.Errorf("embeddedBytecodePath(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

// A key that is not a cache tarball composes no path at all rather than one
// derived from whatever the basename happened to be: nothing embedded can
// answer for it, and a junk path is a stat the init path should never make.
func TestEmbeddedBytecodePath_EmptyForAKeyThatIsNotACacheTarball(t *testing.T) {
	for _, key := range []string{"", "ocel/bytecode/my-app", "ocel/bytecode/my-app/node24.3.1-arm64.tar", "some/other/object.zip"} {
		if got := embeddedBytecodePath(key); got != "" {
			t.Errorf("embeddedBytecodePath(%q) = %q, want no path at all", key, got)
		}
	}
}

func TestLoadEmbeddedBytecodeCache_RoundTrip(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "v24.3.1-x64-abc123-1000"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"index.bin":                        "top level",
		"v24.3.1-x64-abc123-1000/aabbccdd": "nested one",
		"v24.3.1-x64-abc123-1000/11223344": "nested two",
	}
	entries := make([]tarEntry, 0, len(files))
	for name, contents := range files {
		entries = append(entries, tarEntry{name: name, typeflag: tar.TypeReg, content: []byte(contents)})
	}
	tarPath := filepath.Join(t.TempDir(), "node24.3.1-arm64.tar")
	if err := os.WriteFile(tarPath, buildTar(t, entries), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "cache")
	n, hit := loadEmbeddedBytecodeCache(context.Background(), tarPath, dest)
	if !hit {
		t.Fatal("loadEmbeddedBytecodeCache() = false, want a hit")
	}
	var want int64
	for _, contents := range files {
		want += int64(len(contents)) + tarEntryOverhead
	}
	if n != want {
		t.Errorf("loadEmbeddedBytecodeCache() = %d bytes, want %d", n, want)
	}
	for name, contents := range files {
		got, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || string(got) != contents {
			t.Errorf("%s = %q, %v, want %q, nil", name, got, err, contents)
		}
	}
}

// An artifact built without the embed pass is the ordinary case, not a fault:
// the attempt reports a miss, says nothing, and leaves the cache directory
// exactly as it found it for the S3 leg to wipe on its own terms.
func TestLoadEmbeddedBytecodeCache_AbsentTarTouchesNothing(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "left.bin"), []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}

	var hit bool
	out := captureStderr(t, func() {
		_, hit = loadEmbeddedBytecodeCache(context.Background(), filepath.Join(t.TempDir(), "nothing-here.tar"), dest)
	})
	if hit {
		t.Fatal("loadEmbeddedBytecodeCache() = true, want a miss")
	}
	if out != "" {
		t.Errorf("log = %q, want nothing said about an artifact built without the embed pass", out)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "left.bin")); err != nil || string(got) != "untouched" {
		t.Errorf("cache dir = %q, %v, want it left alone", got, err)
	}
}

// A corrupt embedded tar must leave no half-populated cache behind — node
// would trust it — and must not be reported as a hit, so the caller still
// reaches the S3 leg.
func TestLoadEmbeddedBytecodeCache_CorruptLeavesNoDirectory(t *testing.T) {
	tarPath := filepath.Join(t.TempDir(), "node24.3.1-arm64.tar")
	if err := os.WriteFile(tarPath, []byte("this is not a tar stream at all"), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "cache")
	var hit bool
	out := captureStderr(t, func() {
		_, hit = loadEmbeddedBytecodeCache(context.Background(), tarPath, dest)
	})
	if hit {
		t.Fatal("loadEmbeddedBytecodeCache() = true, want a corrupt tar reported as a miss")
	}
	if !strings.Contains(out, tarPath) {
		t.Errorf("log = %q, want it to name the embedded tar", out)
	}
	if info, err := os.Stat(dest); err == nil {
		t.Errorf("dest = %v after a corrupt tar, want it wiped", info.Mode())
	}
}

// The embedded reader shares untarInto's hostile-input validation rather than
// forking it: a tar baked into an artifact is no more trusted than an object
// pulled from S3, and both are checked by the one path.
func TestLoadEmbeddedBytecodeCache_RejectsHostileEntries(t *testing.T) {
	cases := map[string]tarEntry{
		"traversal":     {name: "../escaped.bin", typeflag: tar.TypeReg, content: []byte("nope")},
		"absolute path": {name: "/etc/passwd", typeflag: tar.TypeReg, content: []byte("nope")},
		"symlink":       {name: "link.bin", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			tarPath := filepath.Join(t.TempDir(), "node24.3.1-arm64.tar")
			if err := os.WriteFile(tarPath, buildTar(t, []tarEntry{entry}), 0o644); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "cache")

			var hit bool
			captureStderr(t, func() {
				_, hit = loadEmbeddedBytecodeCache(context.Background(), tarPath, dest)
			})
			if hit {
				t.Fatalf("loadEmbeddedBytecodeCache() = true for a %s entry, want it rejected", name)
			}
			if info, err := os.Stat(dest); err == nil {
				t.Errorf("dest = %v after a rejected entry, want it wiped", info.Mode())
			}
		})
	}
}

// A spent budget must not start an extraction whose result has nowhere to go,
// the same way buildArchiveWithin refuses to start a build.
func TestLoadEmbeddedBytecodeCache_RefusesASpentBudget(t *testing.T) {
	tarPath := filepath.Join(t.TempDir(), "node24.3.1-arm64.tar")
	if err := os.WriteFile(tarPath, buildTar(t, []tarEntry{{name: "blob", typeflag: tar.TypeReg, content: []byte("x")}}), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dest := filepath.Join(t.TempDir(), "cache")
	var hit bool
	captureStderr(t, func() {
		_, hit = loadEmbeddedBytecodeCache(ctx, tarPath, dest)
	})
	if hit {
		t.Fatal("loadEmbeddedBytecodeCache() = true, want a spent budget to skip the attempt")
	}
	if info, err := os.Stat(dest); err == nil {
		t.Errorf("dest = %v, want an untouched directory", info.Mode())
	}
}

// A budget spent *during* the extraction has to end it too, not only one
// already spent when it starts. The two legs share one bytecodeRehydrateBudget
// and this one runs first, so an extraction that runs to completion regardless
// hands the S3 fall-through nothing — and a slow local leg is exactly the case
// where the fall-through is what saves the cold start.
func TestLoadEmbeddedBytecodeCache_StopsWhenTheBudgetRunsOutMidExtraction(t *testing.T) {
	// Enough entries that the extraction is unambiguously longer than the
	// budget: each is a MkdirAll+OpenFile+Close, and the charge against the
	// ceiling (512 B apiece) stays well under it.
	entries := make([]tarEntry, 0, 20000)
	for i := range 20000 {
		entries = append(entries, tarEntry{name: fmt.Sprintf("e/%05d.bin", i), typeflag: tar.TypeReg})
	}
	tarPath := filepath.Join(t.TempDir(), "node24.3.1-arm64.tar")
	if err := os.WriteFile(tarPath, buildTar(t, entries), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "cache")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	var hit bool
	out := captureStderr(t, func() {
		_, hit = loadEmbeddedBytecodeCache(ctx, tarPath, dest)
	})
	if hit {
		t.Fatal("loadEmbeddedBytecodeCache() = true, want the extraction cut off by the shared budget")
	}
	if !strings.Contains(out, tarPath) {
		t.Errorf("log = %q, want it to name the embedded tar", out)
	}
	if info, err := os.Stat(dest); err == nil {
		t.Errorf("dest = %v, want a partial extraction wiped", info.Mode())
	}
}

// The two hits have to be told apart in CloudWatch, so an organic cold start
// says which path it took. The substrings are the contract the e2e assertions
// read.
func TestEmbeddedBytecodeCache_LogsALineDistinctFromTheS3Rehydrate(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".ocel", "bytecode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tarPath := filepath.Join(dir, "node24.3.1-arm64.tar")
	if err := os.WriteFile(tarPath, buildTar(t, []tarEntry{{name: "cached.blob", typeflag: tar.TypeReg, content: []byte("compiled")}}), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "cache")
	var hit bool
	out := captureStderr(t, func() {
		hit = embeddedBytecodeCache(context.Background(), tarPath, dest)
	})
	if !hit {
		t.Fatal("embeddedBytecodeCache() = false, want a hit")
	}
	if !strings.Contains(out, "ocel: loaded embedded compile cache from "+tarPath+":") {
		t.Errorf("log = %q, want it to name the embedded tar", out)
	}
	if strings.Contains(out, "rehydrated compile cache from") {
		t.Errorf("log = %q, want it distinguishable from the S3 rehydrate line", out)
	}
	if !strings.Contains(out, "bytes in") || !strings.Contains(out, "ms") {
		t.Errorf("log = %q, want bytes and elapsed ms", out)
	}
}

// neverEmbedded stands in for a deployment whose artifact carries no embedded
// cache: the ordinary case, and the one every pre-existing budget and
// upload-leg property was written against.
func neverEmbedded(context.Context, *bytecodeResolution) bool { return false }

// An embedded hit is answered locally: no S3 GET, no upload leg, and a source
// the deploy can verify the embed pass by.
func TestBringUpWithBytecode_AnEmbeddedHitSkipsTheS3Leg(t *testing.T) {
	l := newLiveValues(resolves(nil), nil, nil)
	bytecodeReady := make(chan *bytecodeResolution, 1)
	r := &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "assets-xyz", key: "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz"}
	bytecodeReady <- r

	embedded := func(context.Context, *bytecodeResolution) bool { return true }
	rehydrateCalls := 0
	rehydrate := func(context.Context, *bytecodeResolution) bool {
		rehydrateCalls++
		return true
	}

	var gotBudget time.Duration
	membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, embedded, rehydrate)
	if err != nil {
		t.Fatalf("bringUpWithBytecode: %v", err)
	}
	if rehydrateCalls != 0 {
		t.Errorf("rehydrate calls = %d, want the S3 leg skipped entirely", rehydrateCalls)
	}
	if membrane.bytecode != nil {
		t.Error("bytecode != nil, want the upload leg disabled after an embedded hit")
	}
	if !membrane.bytecodeCached() {
		t.Error("bytecodeCached() = false, want the hit recorded")
	}
	if membrane.bytecodeSource != bytecodeSourceEmbedded {
		t.Errorf("bytecodeSource = %q, want %q", membrane.bytecodeSource, bytecodeSourceEmbedded)
	}
	if membrane.bytecodeKey != r.key {
		t.Errorf("bytecodeKey = %q, want the resolution's %q", membrane.bytecodeKey, r.key)
	}
}

// No embedded copy — the artifact was built without the embed pass, or the
// runtime moved on and the baked tar no longer matches the key — leaves the
// S3 leg running exactly as it did before this existed.
func TestBringUpWithBytecode_NoEmbeddedCopyFallsBackToS3(t *testing.T) {
	l := newLiveValues(resolves(nil), nil, nil)
	bytecodeReady := make(chan *bytecodeResolution, 1)
	bytecodeReady <- &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz"}

	rehydrate := func(context.Context, *bytecodeResolution) bool { return true }

	var gotBudget time.Duration
	membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, neverEmbedded, rehydrate)
	if err != nil {
		t.Fatalf("bringUpWithBytecode: %v", err)
	}
	if membrane.bytecodeSource != bytecodeSourceS3 {
		t.Errorf("bytecodeSource = %q, want %q", membrane.bytecodeSource, bytecodeSourceS3)
	}
	if membrane.bytecode != nil {
		t.Error("bytecode != nil, want the upload leg disabled after an S3 hit")
	}
}

// A miss on both legs is the pre-existing story: no source to report, and the
// upload leg armed to publish what this instance compiles.
func TestBringUpWithBytecode_MissOnBothLegsReportsNoSource(t *testing.T) {
	l := newLiveValues(resolves(nil), nil, nil)
	bytecodeReady := make(chan *bytecodeResolution, 1)
	bytecodeReady <- &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "k"}

	var gotBudget time.Duration
	membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, neverEmbedded,
		func(context.Context, *bytecodeResolution) bool { return false })
	if err != nil {
		t.Fatalf("bringUpWithBytecode: %v", err)
	}
	if membrane.bytecodeSource != bytecodeSourceNone {
		t.Errorf("bytecodeSource = %q, want %q", membrane.bytecodeSource, bytecodeSourceNone)
	}
	if membrane.bytecodeCached() {
		t.Error("bytecodeCached() = true, want a miss on both legs")
	}
	if membrane.bytecode == nil {
		t.Fatal("bytecode = nil, want the upload leg armed on a miss")
	}
}

// A corrupt embedded tar must not leave a function permanently cold when S3
// holds a good object — but the S3 leg gets what genuinely remains of the
// rehydrate budget, not a fresh one on top of what the local attempt already
// spent.
func TestBringUpWithBytecode_AFailedEmbeddedAttemptLeavesTheS3LegTheRemainingBudget(t *testing.T) {
	const embeddedCost = 300 * time.Millisecond

	l := newLiveValues(resolves(nil), nil, nil)
	bytecodeReady := make(chan *bytecodeResolution, 1)
	bytecodeReady <- &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "k"}

	embedded := func(ctx context.Context, _ *bytecodeResolution) bool {
		time.Sleep(embeddedCost)
		return false
	}
	var remaining time.Duration
	rehydrate := func(ctx context.Context, _ *bytecodeResolution) bool {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("the S3 leg was handed a context with no deadline, want the shared rehydrate budget")
			return false
		}
		remaining = time.Until(deadline)
		return true
	}

	var gotBudget time.Duration
	if _, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, embedded, rehydrate); err != nil {
		t.Fatalf("bringUpWithBytecode: %v", err)
	}
	if remaining > bytecodeRehydrateBudget-embeddedCost/2 {
		t.Errorf("S3 leg budget = %s, want it short by the %s the embedded attempt spent", remaining, embeddedCost)
	}
	if remaining <= 0 {
		t.Errorf("S3 leg budget = %s, want what remains of the shared budget", remaining)
	}
}

// An unconfigured deployment reaches neither leg.
func TestBringUpWithBytecode_NilResolutionSkipsTheEmbeddedLegToo(t *testing.T) {
	l := newLiveValues(resolves(nil), nil, nil)
	bytecodeReady := make(chan *bytecodeResolution, 1)
	bytecodeReady <- nil

	embeddedCalls := 0
	embedded := func(context.Context, *bytecodeResolution) bool {
		embeddedCalls++
		return true
	}

	var gotBudget time.Duration
	membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, embedded,
		func(context.Context, *bytecodeResolution) bool { return false })
	if err != nil {
		t.Fatalf("bringUpWithBytecode: %v", err)
	}
	if embeddedCalls != 0 {
		t.Errorf("embedded calls = %d, want 0 for an unconfigured deployment", embeddedCalls)
	}
	if membrane.bytecodeSource != bytecodeSourceNone {
		t.Errorf("bytecodeSource = %q, want %q", membrane.bytecodeSource, bytecodeSourceNone)
	}
	if membrane.bytecodeKey != "" {
		t.Errorf("bytecodeKey = %q, want none for an unconfigured deployment", membrane.bytecodeKey)
	}
}

// TestBytecodeEmbedded_TargetsTheDirCompileCacheEnvDeclares pins for the
// embedded leg what its S3 twin pins for the rehydrate: both write where
// NODE_COMPILE_CACHE actually points, by reading the same constant rather
// than a literal of their own.
func TestBytecodeEmbedded_TargetsTheDirCompileCacheEnvDeclares(t *testing.T) {
	t.Setenv(bytecodePrefixEnvVar, "ocel")
	env := compileCacheEnv()
	if len(env) != 1 || !strings.HasPrefix(env[0], "NODE_COMPILE_CACHE=") {
		t.Fatalf("compileCacheEnv() = %v, want exactly one NODE_COMPILE_CACHE entry", env)
	}
	dir := strings.TrimPrefix(env[0], "NODE_COMPILE_CACHE=")
	t.Cleanup(func() { os.RemoveAll(dir) })

	// The path bytecodeEmbedded derives is under /var/task, which no test can
	// write to, so this drives the shared leg directly at the same dir and
	// asserts where it landed.
	tarPath := filepath.Join(t.TempDir(), "node24.3.1-arm64.tar")
	if err := os.WriteFile(tarPath, buildTar(t, []tarEntry{{name: "cached.blob", typeflag: tar.TypeReg, content: []byte("compiled")}}), 0o644); err != nil {
		t.Fatal(err)
	}
	captureStderr(t, func() {
		if !embeddedBytecodeCache(context.Background(), tarPath, compileCacheDir) {
			t.Error("embeddedBytecodeCache() = false, want a hit")
		}
	})
	got, err := os.ReadFile(filepath.Join(dir, "cached.blob"))
	if err != nil || string(got) != "compiled" {
		t.Errorf("file at compileCacheEnv's dir = %q, %v, want %q, nil", got, err, "compiled")
	}
}
