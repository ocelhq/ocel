package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestBytecodeCacheKey(t *testing.T) {
	cases := []struct {
		name         string
		prefix       string
		functionName string
		nodeMajor    int
		goArch       string
		want         string
	}{
		{
			name:         "amd64 maps to the AWS x86_64 spelling",
			prefix:       "ocel",
			functionName: "my-app",
			nodeMajor:    24,
			goArch:       "amd64",
			want:         "ocel/bytecode/my-app/node24-x86_64.tar.gz",
		},
		{
			name:         "arm64 passes through unchanged",
			prefix:       "ocel",
			functionName: "my-app",
			nodeMajor:    24,
			goArch:       "arm64",
			want:         "ocel/bytecode/my-app/node24-arm64.tar.gz",
		},
		{
			name:         "an unrecognized arch still passes through",
			prefix:       "stg/deploy",
			functionName: "other-fn",
			nodeMajor:    20,
			goArch:       "riscv64",
			want:         "stg/deploy/bytecode/other-fn/node20-riscv64.tar.gz",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bytecodeCacheKey(tc.prefix, tc.functionName, tc.nodeMajor, tc.goArch)
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

func TestNodeMajor(t *testing.T) {
	cases := []struct {
		name    string
		version string
		want    int
		wantErr bool
	}{
		{name: "v-prefixed semver", version: "v24.3.1", want: 24},
		{name: "no v prefix", version: "20.11.0", want: 20},
		{name: "double digit major", version: "v18.19.1", want: 18},
		{name: "empty string", version: "", wantErr: true},
		{name: "not a version at all", version: "latest", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nodeMajor(tc.version)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("nodeMajor(%q) = %d, nil, want an error", tc.version, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("nodeMajor(%q) unexpected error: %v", tc.version, err)
			}
			if got != tc.want {
				t.Errorf("nodeMajor(%q) = %d, want %d", tc.version, got, tc.want)
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

	archive, err := buildBytecodeArchive(dir)
	if err != nil {
		t.Fatalf("buildBytecodeArchive: %v", err)
	}

	var wantSize int64
	for _, contents := range files {
		wantSize += int64(len(contents))
	}
	if archive.UncompressedSize != wantSize {
		t.Errorf("UncompressedSize = %d, want %d", archive.UncompressedSize, wantSize)
	}

	got := readArchive(t, archive.Data)
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

	archive, err := buildBytecodeArchive(dir)
	if err != nil {
		t.Fatalf("buildBytecodeArchive: %v", err)
	}
	got := readArchive(t, archive.Data)
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

	archive, err := buildBytecodeArchive(dir)
	if err != nil {
		t.Fatalf("buildBytecodeArchive: %v", err)
	}
	if archive.UncompressedSize != 0 {
		t.Errorf("UncompressedSize = %d, want 0", archive.UncompressedSize)
	}
	got := readArchive(t, archive.Data)
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

// uploadFixture assembles an upload whose flush ack, node version and S3 are
// all under the test's control, and reports how many times the flush was asked
// for.
func uploadFixture(store bytecodeStore, ack compileCacheFlushedPayload, ackOK bool) (*bytecodeUpload, *int) {
	flushes := 0
	u := &bytecodeUpload{
		store:    store,
		bucket:   "assets-xyz",
		prefix:   "ocel",
		function: "my-app",
		arch:     "arm64",
		flush: func(context.Context) (compileCacheFlushedPayload, bool) {
			flushes++
			return ack, ackOK
		},
		nodeVersion: func(context.Context) (string, error) { return "v24.3.1", nil },
	}
	return u, &flushes
}

func TestBytecodeUpload_PutsToTheComposedBucketAndKey(t *testing.T) {
	dir := cacheDirWith(t, "compiled bytes")
	store := &fakeBytecodeStore{}
	u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)

	u.run(context.Background())

	if len(store.heads) != 1 || store.heads[0] != "assets-xyz/ocel/bytecode/my-app/node24-arm64.tar.gz" {
		t.Fatalf("heads = %v, want a single head of the composed key", store.heads)
	}
	if len(store.puts) != 1 {
		t.Fatalf("puts = %d, want 1", len(store.puts))
	}
	put := store.puts[0]
	if put.bucket != "assets-xyz" || put.key != "ocel/bytecode/my-app/node24-arm64.tar.gz" {
		t.Errorf("put to %s/%s, want assets-xyz/ocel/bytecode/my-app/node24-arm64.tar.gz", put.bucket, put.key)
	}
	if got := readArchive(t, put.body); got["cached.blob"] != "compiled bytes" {
		t.Errorf("uploaded archive = %v, want it to carry the cache directory's contents", got)
	}
}

func TestBytecodeUpload_SkipsThePutWhenTheObjectAlreadyExists(t *testing.T) {
	dir := cacheDirWith(t, "compiled bytes")
	store := &fakeBytecodeStore{exists: true}
	u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)

	u.run(context.Background())

	if len(store.heads) != 1 {
		t.Errorf("heads = %v, want the key to have been checked", store.heads)
	}
	if len(store.puts) != 0 {
		t.Errorf("puts = %v, want none once the object exists", store.puts)
	}
}

// A flush that node could not honour — an old runtime, or no cache written —
// means there is nothing on disk worth uploading, so S3 is never touched.
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

			if len(store.heads) != 0 || len(store.puts) != 0 {
				t.Errorf("touched S3 (heads=%v puts=%v), want nothing without a usable flush", store.heads, store.puts)
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

func TestBytecodeUpload_SkipsWhenTheNodeVersionCannotBeRead(t *testing.T) {
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
			store := &fakeBytecodeStore{}
			u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: cacheDirWith(t, "x"), OK: true}, true)
			u.nodeVersion = func(context.Context) (string, error) { return tc.version, tc.err }

			u.run(context.Background())

			if len(store.heads) != 0 || len(store.puts) != 0 {
				t.Errorf("touched S3 (heads=%v puts=%v), want no upload under an unknown major", store.heads, store.puts)
			}
		})
	}
}

// An empty cache directory is the shape of a function whose /tmp was wiped or
// whose flush produced nothing; uploading an empty archive would poison every
// later instance that rehydrated from it.
func TestBytecodeUpload_SkipsAnEmptyCacheDirectory(t *testing.T) {
	store := &fakeBytecodeStore{}
	u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: t.TempDir(), OK: true}, true)

	u.run(context.Background())

	if len(store.heads) != 0 || len(store.puts) != 0 {
		t.Errorf("touched S3 (heads=%v puts=%v), want nothing to upload", store.heads, store.puts)
	}
}

func TestBytecodeUpload_SkipsWhenTheArchiveIsOverTheCeiling(t *testing.T) {
	dir := t.TempDir()
	// Incompressible, so the archive is genuinely over the uncompressed ceiling.
	big := make([]byte, bytecodeCacheCeiling+1)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.blob"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	store := &fakeBytecodeStore{}
	u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)

	u.run(context.Background())

	if len(store.puts) != 0 {
		t.Errorf("puts = %v, want none over the ceiling", store.puts)
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

// The same context bounds the flush, the HEAD and the PUT, so an S3 call that
// hangs is cancelled at the budget rather than running past the deadline the
// platform is about to enforce.
func TestBytecodeUpload_BoundsTheStoreCallsByTheBudget(t *testing.T) {
	store := &blockingBytecodeStore{}
	u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: cacheDirWith(t, "x"), OK: true}, true)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(completionMargin+50*time.Millisecond))
	defer cancel()

	start := time.Now()
	u.run(ctx)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s, want the store call cancelled at the budget", elapsed)
	}
}

// blockingBytecodeStore never answers, so only the context can end the call.
type blockingBytecodeStore struct{}

func (blockingBytecodeStore) objectExists(ctx context.Context, _, _ string) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func (blockingBytecodeStore) putObject(ctx context.Context, _, _ string, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
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

// The gate and its neighbours are read before any AWS config is loaded, so a
// function missing one of them never constructs a client at all.
func TestResolveBytecodeUpload_NilWhenNotFullyConfigured(t *testing.T) {
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
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(bytecodePrefixEnvVar, tc.prefix)
			t.Setenv("OCEL_ISR_BUCKET", tc.bucket)
			t.Setenv("AWS_LAMBDA_FUNCTION_NAME", tc.function)
			if got := resolveBytecodeUpload(context.Background(), nil); got != nil {
				t.Errorf("resolveBytecodeUpload() = %+v, want nil", got)
			}
		})
	}
}

// controlConnPair wires a Membrane to a stand-in for the node child over a real
// connection, with the drain loop running exactly as startNode runs it.
func controlConnPair(t *testing.T) (*Membrane, *bufio.Reader, net.Conn) {
	t.Helper()
	membraneSide, nodeSide := net.Pipe()
	t.Cleanup(func() { membraneSide.Close(); nodeSide.Close() })
	m := &Membrane{control: membraneSide}
	go m.drainControl(bufio.NewReader(membraneSide))
	return m, bufio.NewReader(nodeSide), nodeSide
}

func TestFlushCompileCache_DeliversTheAckToTheWaiter(t *testing.T) {
	m, nodeReader, nodeConn := controlConnPair(t)
	go func() {
		line, err := nodeReader.ReadString('\n')
		if err != nil {
			return
		}
		if line != flushCompileCacheLine {
			t.Errorf("node received %q, want %q", line, flushCompileCacheLine)
		}
		fmt.Fprintln(nodeConn, `{"type":"compile-cache-flushed","payload":{"dir":"/tmp/.ocel/compile-cache","ok":true}}`)
	}()

	ack, ok := m.flushCompileCache(context.Background())
	if !ok {
		t.Fatal("flushCompileCache() ok = false, want the ack delivered")
	}
	if ack.Dir != "/tmp/.ocel/compile-cache" || !ack.OK {
		t.Errorf("ack = %+v, want the dir and ok node reported", ack)
	}
}

// Node reports a null dir when it has no compile cache API at all, which must
// read as not-ok rather than as a directory named "null".
func TestFlushCompileCache_CarriesANullDirAsNotOK(t *testing.T) {
	m, nodeReader, nodeConn := controlConnPair(t)
	go func() {
		if _, err := nodeReader.ReadString('\n'); err != nil {
			return
		}
		fmt.Fprintln(nodeConn, `{"type":"compile-cache-flushed","payload":{"dir":null,"ok":false}}`)
	}()

	ack, ok := m.flushCompileCache(context.Background())
	if !ok {
		t.Fatal("flushCompileCache() ok = false, want the ack delivered")
	}
	if ack.Dir != "" || ack.OK {
		t.Errorf("ack = %+v, want an empty dir and ok=false", ack)
	}
}

// A wedged child must not hold the runtime loop off /next, so the wait ends on
// the caller's context even before the flush cap would fire.
func TestFlushCompileCache_GivesUpWhenTheChildNeverAnswers(t *testing.T) {
	m, nodeReader, _ := controlConnPair(t)
	go nodeReader.ReadString('\n') // read the request, answer nothing

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, ok := m.flushCompileCache(ctx); ok {
		t.Error("flushCompileCache() ok = true, want a give-up")
	}
	if elapsed := time.Since(start); elapsed > compileCacheFlushTimeout {
		t.Errorf("took %s, want the context to end the wait early", elapsed)
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
	m.pending = map[string]chan struct{}{}
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
