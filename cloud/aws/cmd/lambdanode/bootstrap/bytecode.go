package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// bytecodeCacheCeiling bounds what the membrane will upload: an S3 PUT that
// grows unbounded with every cold start is worse than a warm start that never
// gets a cache, so the caller checks the archive against this before it ever
// reaches for a client.
const bytecodeCacheCeiling = 64 << 20 // 64 MiB

// bytecodeCacheKey composes the S3 key the membrane uploads a function's
// compile cache under and downloads it back from. prefix, functionName and
// nodeVersion are the caller's to supply — nothing here reads the
// environment, which is what keeps it callable from a test with no AWS
// client in sight. nodeVersion is expected already canonical; this function
// does not clean it.
func bytecodeCacheKey(prefix, functionName, nodeVersion, goArch string) string {
	return fmt.Sprintf("%s/bytecode/%s/node%s-%s.tar.gz", prefix, functionName, nodeVersion, s3Arch(goArch))
}

// s3Arch renders a Go GOARCH value the way AWS spells it in its own naming
// (Lambda architecture, S3 object keys): amd64 is x86_64, everything else
// (arm64 included) already matches and passes through unchanged.
func s3Arch(goArch string) string {
	if goArch == "amd64" {
		return "x86_64"
	}
	return goArch
}

var nodeVersionPattern = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)$`)

// canonicalNodeVersion cleans and validates a Node version string such as
// "v24.3.1" or "24.3.1", returning it without a leading "v". The membrane
// cannot reliably learn the child's version from the flush ack, so this only
// ever parses a version the caller obtained some other way; anything that is
// not three dot-separated numbers returns an error rather than a guess, since
// a garbled version must never compose a junk S3 key.
func canonicalNodeVersion(version string) (string, error) {
	m := nodeVersionPattern.FindStringSubmatch(version)
	if m == nil {
		return "", fmt.Errorf("not a node version: %q", version)
	}
	return m[1], nil
}

// exceedsBytecodeCacheCeiling reports whether an archive is too large to
// upload. The ceiling is the caller's decision to act on — this only answers
// the question; it never trims the archive to fit, because a truncated
// compile cache is a corrupt one.
func exceedsBytecodeCacheCeiling(uncompressedSize int64) bool {
	return uncompressedSize > bytecodeCacheCeiling
}

// buildBytecodeArchive walks dir and tars+gzips every regular file it finds,
// preserving the paths relative to dir (Node nests its output under a
// version-hash subdirectory, so the structure carries that along). A dir that
// does not exist yields an empty archive rather than an error: no compile
// cache is not a failure the caller needs to swallow, it is simply nothing to
// upload yet.
//
// ctx stops the walk between entries. The caller's budget is the whole reason:
// abandoning the wait while the work ran on would leave a goroutine holding a
// gzip buffer and burning CPU, which on Lambda resumes when the sandbox thaws
// and competes with a later request — the opposite of what this feature is for.
func buildBytecodeArchive(ctx context.Context, dir string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return nil // caller passed a dir that doesn't exist yet
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{
			Name:    filepath.ToSlash(rel),
			Size:    info.Size(),
			Mode:    int64(info.Mode().Perm()),
			ModTime: info.ModTime(),
		}); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(tw, f); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("build bytecode archive from %s: %w", dir, err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close bytecode archive tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("close bytecode archive gzip: %w", err)
	}
	return buf.Bytes(), nil
}

// compileCacheDir is where node is told to write its V8 compile cache. It sits
// under /tmp because that is the only writable path in the sandbox, and it is
// namespaced so nothing else the function writes there can be swept into the
// archive.
const compileCacheDir = "/tmp/.ocel/compile-cache"

// bytecodePrefixEnvVar is the whole gate. A deployment that does not set it gets
// no compile cache at all — not the upload, not even NODE_COMPILE_CACHE on the
// child — so the feature is off until a deploy turns it on.
const bytecodePrefixEnvVar = "OCEL_BYTECODE_PREFIX"

// bytecodeBucketEnvVar names the account-global bucket the compile cache lives
// in. It is its own variable rather than a reuse of OCEL_ISR_BUCKET — that one
// is unset for any function with no ISR cache, which used to be every
// non-Next function this feature now also has to reach. There is no fallback
// chain: a deploy that sets bytecodePrefixEnvVar always sets this alongside
// it (see functionEnv on the deploy side), so an unset bucket here reads as
// exactly what resolveBytecodeResolution already treats it as — the gate off.
const bytecodeBucketEnvVar = "OCEL_BYTECODE_BUCKET"

// bytecodeUploadBudget caps what the upload may spend after an invocation is
// already served. It is billed duration, not request latency, but the sandbox is
// still holding the runtime loop off /next, so the cap is short and the
// invocation deadline shortens it further.
const bytecodeUploadBudget = 2 * time.Second

// bytecodeRehydrateBudget caps the read leg the same way. It runs before the
// spawn and inside startupBudget rather than after it, so this is carved out
// of the 8s node's own boot has to fit in, not added on top of it — 2s is
// enough for an archive well under the ceiling on a cold network path,
// without leaving node so little of the budget that a slow S3 GET turns into
// an init timeout of its own.
const bytecodeRehydrateBudget = 2 * time.Second

// bytecodeResolveBudget bounds the version probe and the AWS config load
// that precede the key composition, both of which now run before the spawn
// rather than off the request path. Unlike the rehydrate and upload legs,
// this is not carved out of startupBudget by subtraction — it typically
// costs nothing extra, since it overlaps the live-values prefetch and the
// baked-var decrypts — but a wedged exec or a stalled config load must still
// land in the same place the off switch does rather than hold up the join,
// and therefore the spawn, indefinitely. It bounds the context handed to
// resolveBytecodeResolution and, independently, the join itself: whichever
// of those two never respects the other still gives up here.
const bytecodeResolveBudget = 2 * time.Second

// compileCacheFlushTimeout bounds the wait for node's flush ack. A child that
// never answers is a child that is wedged, and the loop must not join it there.
const compileCacheFlushTimeout = time.Second

// compileCacheEnv is what node is told at exec, and only when the gate is open.
// Node reads NODE_COMPILE_CACHE at startup and ignores it on versions that do
// not know it, which is what makes an old runtime degrade without a version
// check anywhere.
func compileCacheEnv() []string {
	if os.Getenv(bytecodePrefixEnvVar) == "" {
		return nil
	}
	return []string{"NODE_COMPILE_CACHE=" + compileCacheDir}
}

// bytecodeStore is the S3 surface the upload and rehydration need, and
// nothing more: does the object already exist, write it, and read it back.
// Narrowing it to three calls is what lets every test here run against a
// fake with no AWS client, config or credentials in reach.
type bytecodeStore interface {
	objectExists(ctx context.Context, bucket, key string) (bool, error)
	putObject(ctx context.Context, bucket, key string, body []byte) error
	getObject(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error)
}

// errBytecodeCacheMiss is what getObject returns for an absent key. It is the
// expected state on a deployment's first cold start, so rehydrateCompileCache
// tests for it with errors.Is and logs it as a miss rather than a fault.
var errBytecodeCacheMiss = errors.New("bytecode cache: no object at that key")

type s3BytecodeStore struct{ client *s3.Client }

// objectExists answers the probe that decides whether this instance has to pay
// for an upload at all. Absence has two spellings here, not one: S3 only
// answers 404 for a missing key when the caller may also list the bucket, and
// this function's role deliberately holds s3:GetObject and s3:PutObject on its
// own prefix and nothing else (isrPolicy). With no s3:ListBucket, an absent key
// comes back 403 — so reading 403 as a fault made the first cold start of every
// fresh deployment fail its warm pass and publish nothing, which is the one
// state the whole feature exists to leave behind.
//
// Reading 403 as absent is safe in the direction that matters: the only thing
// downstream of a false "absent" is an upload, and a PUT the role is genuinely
// not allowed to make fails on its own and is reported there.
func (s s3BytecodeStore) objectExists(ctx context.Context, bucket, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		var notFound *s3types.NotFound
		if errors.As(err, &notFound) || isS3Forbidden(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// isS3Forbidden reports whether err is S3 answering 403. HeadObject has no
// modelled error shape for it — the SDK surfaces a bare
// smithyhttp.ResponseError — so the status code is what there is to match on.
func isS3Forbidden(err error) bool {
	var resp *awshttp.ResponseError
	return errors.As(err, &resp) && resp.HTTPStatusCode() == http.StatusForbidden
}

func (s s3BytecodeStore) putObject(ctx context.Context, bucket, key string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &bucket,
		Key:    &key,
		Body:   bytes.NewReader(body),
	})
	return err
}

func (s s3BytecodeStore) getObject(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		var noSuchKey *s3types.NoSuchKey
		var notFound *s3types.NotFound
		if errors.As(err, &noSuchKey) || errors.As(err, &notFound) {
			return nil, 0, errBytecodeCacheMiss
		}
		return nil, 0, err
	}
	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	return out.Body, size, nil
}

// bytecodeUpload is the once-per-instance publish of this function's compile
// cache. bucket and key come from the same bytecodeResolution the rehydrate
// leg reads, never recomposed here — a version drift between the two legs
// would be a silent, permanent cache miss, and holding one shared value
// instead of two derivations is what makes that impossible rather than
// merely correct today. Every other dependency on the outside world is a
// field too, so the whole sequence is exercisable without a node child, an
// AWS client or the environment.
type bytecodeUpload struct {
	store  bytecodeStore
	bucket string
	key    string
	// root is NODE_COMPILE_CACHE itself, which is what the read leg extracts
	// into and therefore what the archive must be rooted at. It is not what
	// node's flush ack names: getCompileCacheDir reports a subdirectory of
	// this one, keyed by node version, architecture, V8 flag hash and uid.
	root  string
	flush func(ctx context.Context) (compileCacheFlushedPayload, bool)
}

// bytecodeUploadOutcome is what an attempt has to say for itself. The
// post-invocation path discards it — the invocation was already served, and a
// line on stderr is all anyone can act on there — but a deploy-time warm
// invocation reports it back, where "published" has to be distinguishable from
// "over the ceiling" and from "the PUT failed": those are the same silence to
// a caller that only sees whether the invocation succeeded.
type bytecodeUploadOutcome struct {
	uploaded bool
	existed  bool
	bytes    int64
	reason   string
}

// abandonUpload ends an attempt with the one line it needs on stderr and
// the same words carried back to a caller that reports rather than logs.
func abandonUpload(format string, args ...any) bytecodeUploadOutcome {
	reason := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, "ocel: "+reason)
	return bytecodeUploadOutcome{reason: reason}
}

// run publishes the cache, or gives up. Nothing it does can fail an invocation:
// every leg that can go wrong ends the attempt with a line on stderr, because a
// warm start that never gets a cache is strictly better than a request that
// pays for one.
//
// The HEAD runs before the flush and the archive build, not after: an
// instance that lost the upload race to another instance finds out for the
// price of a HEAD, rather than paying to flush node's cache and build an
// archive it is only going to discard.
func (u bytecodeUpload) run(ctx context.Context) bytecodeUploadOutcome {
	budget := bytecodeBudget(ctx)
	if budget <= 0 {
		return abandonUpload("no time left to publish the compile cache; skipping")
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	exists, err := u.store.objectExists(ctx, u.bucket, u.key)
	if err != nil {
		return abandonUpload("could not check for an existing compile cache at %s: %v", u.key, err)
	}
	if exists {
		return bytecodeUploadOutcome{existed: true}
	}

	ack, ok := u.flush(ctx)
	if !ok {
		// flushCompileCache already said why on stderr.
		return bytecodeUploadOutcome{reason: "node did not acknowledge the compile-cache flush"}
	}
	if !ack.OK {
		return abandonUpload("node reported no compile cache to flush; skipping upload")
	}

	// Measured and archived at the root, never at what node reported: the two
	// differ by the version-and-flags subdirectory node keeps its entries
	// under, and that level has to survive the round trip or the read leg
	// restores a cache node never looks at.
	if !within(u.root, ack.Dir) {
		return abandonUpload("node reported a compile cache at %s, outside %s; skipping upload", ack.Dir, u.root)
	}

	size, err := compileCacheSize(ctx, u.root)
	if err != nil {
		return abandonUpload("could not measure the compile cache: %v", err)
	}
	if size == 0 {
		// The two mean different things: a directory node never created points
		// at the flush, one that exists and holds nothing points at the app.
		if _, err := os.Stat(ack.Dir); os.IsNotExist(err) {
			return abandonUpload("node reported a compile cache at %s but nothing is there; skipping upload", ack.Dir)
		}
		return abandonUpload("the compile cache at %s is empty; nothing to upload", ack.Dir)
	}
	if exceedsBytecodeCacheCeiling(size) {
		over := abandonUpload("compile cache is %d bytes, over the %d byte ceiling; skipping upload", size, bytecodeCacheCeiling)
		over.bytes = size
		return over
	}

	archive, err := buildArchiveWithin(ctx, u.root)
	if err != nil {
		return abandonUpload("could not archive the compile cache: %v", err)
	}

	if err := u.store.putObject(ctx, u.bucket, u.key, archive); err != nil {
		return abandonUpload("could not upload the compile cache to %s: %v", u.key, err)
	}
	return bytecodeUploadOutcome{uploaded: true, bytes: size}
}

// within reports whether path is root or sits beneath it, which is the one
// thing the upload leg needs to know about the directory node hands back: an
// ack from somewhere else means node was told a different NODE_COMPILE_CACHE
// than the read leg will restore into, and no archive can bridge that.
func within(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// compileCacheSize sums what a compile cache directory holds without reading a
// byte of it, so the ceiling can be enforced before the walk-read-gzip that
// would otherwise do all that work only to throw it away. A directory that does
// not exist sums to zero, matching how buildBytecodeArchive treats it.
//
// Each file is charged tarEntryOverhead in addition to its payload, matching
// what untarInto charges per entry on the other side of the same ceiling: the
// upload gate and the rehydrate gate are the same 64 MiB against two different
// arithmetics otherwise, and an archive that passes this one but fails that
// one is an object that uploads once and then wipes every cold start that
// tries to read it back, forever, since the upload leg's HEAD then always
// finds it already there.
//
// ctx stops the walk between entries, for the same reason the build's does: no
// leg of the attempt may outlive the budget the caller handed it.
func compileCacheSize(ctx context.Context, dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size() + tarEntryOverhead
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measure compile cache at %s: %w", dir, err)
	}
	return total, nil
}

// buildArchiveWithin abandons the archive if the budget runs out mid-build. The
// read and gzip are the most expensive leg, so on a memory-starved sandbox they
// can outlast the deadline — and a runtime loop still waiting on them is an
// invocation Lambda records as a timeout after it was already answered.
//
// The build stops on the same context, so this releases the caller and ends the
// work. The select is what makes the release immediate rather than delayed to
// the walk's next entry, which matters when a single file is large.
func buildArchiveWithin(ctx context.Context, dir string) ([]byte, error) {
	// Checked before the goroutine rather than left to the select below, which
	// picks at random when both cases are ready: a budget already spent must
	// never start a build whose result has nowhere to go.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("no budget left to archive %s: %w", dir, err)
	}

	type result struct {
		archive []byte
		err     error
	}
	done := make(chan result, 1)
	go func() {
		archive, err := buildBytecodeArchive(ctx, dir)
		done <- result{archive: archive, err: err}
	}()
	select {
	case r := <-done:
		return r.archive, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("archiving %s outlasted the upload budget: %w", dir, ctx.Err())
	}
}

// tarEntryOverhead is charged against the ceiling for every entry regardless
// of its declared size. A tar stream costs at least one 512-byte header block
// per entry no matter how little payload follows it, so without this an
// archive of empty entries would never advance the running total: the
// ContentLength precheck admits up to the ceiling compressed, and gzip's
// ratio on a repetitive stream can turn that into orders of magnitude more
// tar bytes, each entry a real MkdirAll+OpenFile+Close under dir. Charging
// the header cost up front makes the entry count itself bounded by the
// ceiling, closing that off without a separate counter.
const tarEntryOverhead = 512

// untarGzipInto gunzips r and hands the plain stream to untarInto. The gzip
// layer is exactly this much of the read leg: the S3 object is compressed
// because it crosses the network, and the copy baked into the deployment
// package is not, because Lambda's own unzip already decompressed it before
// INIT. Both are the same tar, extracted and validated by the one path below.
func untarGzipInto(ctx context.Context, r io.Reader, dir string, ceiling int64) (int64, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, fmt.Errorf("bytecode cache is not gzip: %w", err)
	}
	defer gz.Close()
	return untarInto(ctx, gz, dir, ceiling)
}

// untarInto untars r into dir, streaming throughout —
// never buffering the archive or an entry in memory — and returns the total
// bytes written. dir is trusted; every entry is not: a name that is absolute,
// climbs out via "..", or resolves to dir itself (an empty name, ".", "./")
// is rejected before anything is created, and only regular files are
// written, so a symlink, hardlink or device planted in the archive cannot
// land outside dir, replace dir, or land as something other than a plain
// file. ceiling bounds the running total — charged per entry as well as per
// byte, see tarEntryOverhead — aborting mid-stream rather than after the
// fact, so a hostile or corrupt archive cannot fill the sandbox's disk.
//
// ctx stops the extraction between entries. Both callers share one budget
// across both rehydration legs, so the cost of running on past it is not just
// an abandoned goroutine but the leg that comes next: an extraction that spends
// the whole of bytecodeRehydrateBudget hands the S3 fall-through nothing at
// all, and a function whose embedded tar turned out to be corrupt is exactly
// the one that needs S3 most. Per entry is where the check goes because that is
// where the work is — a ceiling-bounded archive is many small files, not one
// long read.
func untarInto(ctx context.Context, r io.Reader, dir string, ceiling int64) (int64, error) {
	tr := tar.NewReader(r)

	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, fmt.Errorf("extracting the bytecode cache ran out of time: %w", err)
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, fmt.Errorf("read bytecode cache entry: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			return total, fmt.Errorf("bytecode cache entry %q is not a regular file", hdr.Name)
		}
		if filepath.IsAbs(hdr.Name) {
			return total, fmt.Errorf("bytecode cache entry %q is an absolute path", hdr.Name)
		}
		rel := filepath.Clean(hdr.Name)
		if rel == "." {
			return total, fmt.Errorf("bytecode cache entry %q resolves to the cache directory itself", hdr.Name)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return total, fmt.Errorf("bytecode cache entry %q escapes the target directory", hdr.Name)
		}

		total += hdr.Size + tarEntryOverhead
		if total > ceiling {
			return total, fmt.Errorf("bytecode cache exceeds the %d byte ceiling", ceiling)
		}

		target := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return total, fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		// The mode is fixed rather than taken from hdr: the upload leg only ever
		// writes something in the 0644 family, so an archive claiming otherwise
		// is corrupt or hostile, and honouring it risks nothing worse than a
		// cache file this runtime user cannot read back — a silent degradation
		// this avoids by construction instead of validating for it.
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return total, fmt.Errorf("create %s: %w", target, err)
		}
		_, copyErr := io.CopyN(f, tr, hdr.Size)
		closeErr := f.Close()
		if copyErr != nil {
			return total, fmt.Errorf("write %s: %w", target, copyErr)
		}
		if closeErr != nil {
			return total, fmt.Errorf("close %s: %w", target, closeErr)
		}
	}
}

// rehydrateCompileCache is the whole read leg: wipe dir, fetch the archive at
// bucket/key, and stream-extract it back into dir. It is the upload's mirror
// image, and every failure mode lands the same way — a log line and dir left
// absent — because init must boot exactly as it does with the feature off; a
// half-populated cache directory would be worse than none, since node would
// trust it.
//
// ctx bounds the whole attempt on the caller's terms: rehydration owns no
// timeout of its own, so a caller that cancels gets the extraction stopped
// and dir cleaned up rather than a wedged read outliving the deadline it was
// given.
func rehydrateCompileCache(ctx context.Context, store bytecodeStore, bucket, key, dir string) (int64, bool) {
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not clear %s before rehydrating the compile cache: %v\n", dir, err)
		return 0, false
	}

	body, size, err := store.getObject(ctx, bucket, key)
	if err != nil {
		if errors.Is(err, errBytecodeCacheMiss) {
			fmt.Fprintf(os.Stderr, "ocel: no compile cache at %s yet; nothing to rehydrate\n", key)
		} else {
			fmt.Fprintf(os.Stderr, "ocel: could not fetch the compile cache at %s: %v\n", key, err)
		}
		return 0, false
	}
	defer body.Close()

	// size is compressed, and the ceiling is an uncompressed bound, so this is
	// a cheap early-out rather than the real check: it is strictly conservative
	// (compressed bytes never exceed uncompressed), and untarInto's own running
	// total is what actually enforces the ceiling as entries stream in.
	if exceedsBytecodeCacheCeiling(size) {
		fmt.Fprintf(os.Stderr, "ocel: compile cache at %s is %d bytes, over the %d byte ceiling; skipping rehydration\n",
			key, size, bytecodeCacheCeiling)
		return 0, false
	}

	type result struct {
		n   int64
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := untarGzipInto(ctx, body, dir, bytecodeCacheCeiling)
		done <- result{n: n, err: err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "ocel: could not rehydrate the compile cache from %s: %v\n", key, r.err)
			os.RemoveAll(dir)
			return 0, false
		}
		return r.n, true
	case <-ctx.Done():
		// Closing body is what frees the goroutine: it is blocked on a Read
		// that only the context knows should stop, and only closing the
		// stream it is reading interrupts that call.
		body.Close()
		<-done
		fmt.Fprintf(os.Stderr, "ocel: rehydrating the compile cache from %s ran out of time: %v\n", key, ctx.Err())
		os.RemoveAll(dir)
		return 0, false
	}
}

// bytecodeSource is where an instance's compile cache came from. A deploy-time
// warm invocation is the caller that needs it: the two hits are the same
// already-cached answer otherwise, and a deploy that just embedded a cache into
// an artifact has no other way to tell whether the function actually read it.
//
// It is a named type rather than a bare string so the three values below are
// the only things that can be assigned to a field declaring one — the wire
// spelling is deliberately duplicated on the deploy side (see warm.go), and a
// duplicated constant is only safe while each side's own uses are checked.
type bytecodeSource string

const (
	bytecodeSourceEmbedded bytecodeSource = "embedded"
	bytecodeSourceS3       bytecodeSource = "s3"
	bytecodeSourceNone     bytecodeSource = "none"
)

// embeddedBytecodeDir is where the deploy bakes this deployment's compile
// cache into the function's own artifact, under the same .ocel/ overlay
// namespace the variable files travel in. Lambda unpacks the deployment
// package before INIT — unbilled, and outside the init ceiling — so a tar
// read from here costs a local read where the object costs a network round
// trip. It is read-only, which is why the tar is still extracted into
// compileCacheDir rather than pointed at in place.
const embeddedBytecodeDir = "/var/task/.ocel/bytecode"

// embeddedBytecodePath is where the deploy would have baked the object at
// key, or "" for a key that names no cache tarball. It is derived from the
// key the resolution already composed — never from a version and an arch read
// again here — because a second derivation is exactly the drift
// bytecodeResolution exists to rule out: it would surface after a runtime bump
// as an instance loading a stale embedded cache under a fresh key, which is
// the one failure this feature cannot recover from on its own.
func embeddedBytecodePath(key string) string {
	base := path.Base(key)
	if !strings.HasSuffix(base, ".tar.gz") {
		return ""
	}
	return filepath.Join(embeddedBytecodeDir, strings.TrimSuffix(base, ".gz"))
}

// loadEmbeddedBytecodeCache extracts the tar baked into the deployment package
// at tarPath into dir. It is the S3 leg's local twin, with one difference that
// matters: an absent tar says nothing at all, because an artifact built
// without the embed pass is the ordinary case rather than a miss worth a line,
// and it leaves dir exactly as it found it for the S3 leg to wipe on its own
// terms.
//
// Every other outcome wipes dir and reports a miss. A half-populated cache
// directory is worse than none — node would trust it — and the caller has to
// be free to fall through: a corrupt embedded copy may never leave a function
// permanently cold when S3 holds a good object.
//
// ctx bounds the extraction as it proceeds, not only before it starts. That a
// local read cannot block the way an S3 body read can is beside the point: this
// leg and the S3 leg share one bytecodeRehydrateBudget, and a tar near the
// ceiling is tens of thousands of writes into /tmp on a sandbox whose IO is
// sized by its memory. An extraction that spends the whole budget leaves the
// fall-through none — which is precisely the case where the fall-through is the
// only thing that can still save the cold start.
func loadEmbeddedBytecodeCache(ctx context.Context, tarPath, dir string) (int64, bool) {
	if ctx.Err() != nil {
		return 0, false
	}

	f, err := os.Open(tarPath)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "ocel: could not open the embedded compile cache at %s: %v\n", tarPath, err)
		}
		return 0, false
	}
	defer f.Close()

	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not clear %s before loading the embedded compile cache at %s: %v\n", dir, tarPath, err)
		return 0, false
	}

	n, err := untarInto(ctx, f, dir, bytecodeCacheCeiling)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not load the embedded compile cache at %s: %v\n", tarPath, err)
		os.RemoveAll(dir)
		return 0, false
	}
	return n, true
}

// embeddedBytecodeCache runs the local leg and logs the one line a hit needs.
// It is deliberately worded apart from the S3 leg's "rehydrated compile cache
// from": an organic cold start is only attributable in CloudWatch if the two
// paths read differently, and the deploy's verify invoke is not the only place
// that has to tell them apart.
func embeddedBytecodeCache(ctx context.Context, tarPath, dir string) bool {
	start := time.Now()
	n, hit := loadEmbeddedBytecodeCache(ctx, tarPath, dir)
	if hit {
		fmt.Fprintf(os.Stderr, "ocel: loaded embedded compile cache from %s: %d bytes in %dms\n",
			tarPath, n, time.Since(start).Milliseconds())
	}
	return hit
}

// bytecodeBudget is what the upload may spend: the cap, or whatever is left
// before the invocation deadline minus the margin the runtime needs to call
// /next, whichever is smaller. A non-positive result means the deadline is
// already too close to start at all.
func bytecodeBudget(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return bytecodeUploadBudget
	}
	if remaining := time.Until(deadline) - completionMargin; remaining < bytecodeUploadBudget {
		return remaining
	}
	return bytecodeUploadBudget
}

// nodeVersionFromBinary asks the same binary the child was exec'd from what
// version it is. It now runs on the init critical path, before the spawn, as
// part of composing the compile cache's key — not off the request path the
// way the upload's own exec used to be. It is bounded by whatever context the
// caller hands it: resolveBytecodeResolution's caller derives that context
// from bytecodeResolveBudget, so a wedged exec cannot hold up the join (and
// therefore the spawn) past that cap.
func nodeVersionFromBinary(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, nodeBinaryPath, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// bytecodeResolution is this deployment's bytecode cache identity, resolved
// exactly once and held by both legs from then on: the rehydrate leg reads
// bucket and key directly, and upload composes the same key back into a
// bytecodeUpload via the method below. Composing the key a second time
// anywhere else would risk the one failure this feature cannot recover from
// on its own — a version drift between the two legs is a silent, permanent
// cache miss — so this type exists to make that structurally impossible
// rather than merely correct today.
type bytecodeResolution struct {
	store  bytecodeStore
	bucket string
	key    string
}

// upload builds this instance's upload leg from the resolution, wiring in
// the one dependency the resolution cannot supply for itself: the flush
// function, which needs a live Membrane that does not exist until after
// bringUp returns.
func (r *bytecodeResolution) upload(flush func(context.Context) (compileCacheFlushedPayload, bool)) *bytecodeUpload {
	return &bytecodeUpload{store: r.store, bucket: r.bucket, key: r.key, root: compileCacheDir, flush: flush}
}

// resolveBytecodeResolution builds the bytecode cache identity this
// deployment is configured for, or nil for one that is configured for none.
// Nil is the off switch every caller checks, so an unset prefix, a missing
// bucket, a missing function name, a node version this process cannot read
// or that doesn't parse, or an AWS config that will not load all land in the
// same place: the membrane simply never tries, on either leg.
//
// nodeVersion is a field for the same reason it was one on bytecodeUpload
// before this replaced it there: the whole resolution is exercisable without
// a node binary, an AWS client or the environment in reach.
func resolveBytecodeResolution(ctx context.Context, nodeVersion func(context.Context) (string, error)) *bytecodeResolution {
	prefix := os.Getenv(bytecodePrefixEnvVar)
	bucket := os.Getenv(bytecodeBucketEnvVar)
	function := os.Getenv("AWS_LAMBDA_FUNCTION_NAME")
	if prefix == "" || bucket == "" || function == "" {
		return nil
	}

	version, err := nodeVersion(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not read node's version, compile cache disabled: %v\n", err)
		return nil
	}
	canonical, err := canonicalNodeVersion(version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: %v, compile cache disabled\n", err)
		return nil
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: no aws config for the compile cache: %v\n", err)
		return nil
	}

	return &bytecodeResolution{
		store:  s3BytecodeStore{client: s3.NewFromConfig(cfg)},
		bucket: bucket,
		key:    bytecodeCacheKey(prefix, function, canonical, runtime.GOARCH),
	}
}

// rehydrateBytecodeCache runs the read leg under its own cap and logs the
// one line the outcome needs. A miss already names the key and the reason —
// that is rehydrateCompileCache's own log line — so only the hit is logged
// here, with what a miss's line cannot know yet: the bytes restored and how
// long it took.
func rehydrateBytecodeCache(ctx context.Context, r *bytecodeResolution, dir string) bool {
	ctx, cancel := context.WithTimeout(ctx, bytecodeRehydrateBudget)
	defer cancel()

	start := time.Now()
	n, hit := rehydrateCompileCache(ctx, r.store, r.bucket, r.key, dir)
	if hit {
		fmt.Fprintf(os.Stderr, "ocel: rehydrated compile cache from %s: %d bytes in %dms\n",
			r.key, n, time.Since(start).Milliseconds())
	}
	return hit
}
