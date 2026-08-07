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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

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

// bytecodeUploadBudget caps what the upload may spend after an invocation is
// already served. It is billed duration, not request latency, but the sandbox is
// still holding the runtime loop off /next, so the cap is short and the
// invocation deadline shortens it further.
const bytecodeUploadBudget = 2 * time.Second

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

// bytecodeStore is the S3 surface the upload needs, and nothing more: does the
// object already exist, and write it if not. Narrowing it to two calls is what
// lets every test here run against a fake with no AWS client, config or
// credentials in reach.
type bytecodeStore interface {
	objectExists(ctx context.Context, bucket, key string) (bool, error)
	putObject(ctx context.Context, bucket, key string, body []byte) error
}

type s3BytecodeStore struct{ client *s3.Client }

func (s s3BytecodeStore) objectExists(ctx context.Context, bucket, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		var notFound *s3types.NotFound
		if errors.As(err, &notFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s s3BytecodeStore) putObject(ctx context.Context, bucket, key string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &bucket,
		Key:    &key,
		Body:   bytes.NewReader(body),
	})
	return err
}

// bytecodeUpload is the once-per-instance publish of this function's compile
// cache. Every dependency it has on the outside world is a field, so the whole
// sequence is exercisable without a node child, an AWS client or the
// environment.
type bytecodeUpload struct {
	store       bytecodeStore
	bucket      string
	prefix      string
	function    string
	arch        string
	flush       func(ctx context.Context) (compileCacheFlushedPayload, bool)
	nodeVersion func(ctx context.Context) (string, error)
}

// run publishes the cache, or gives up. Nothing it does can fail an invocation:
// every leg that can go wrong ends the attempt with a line on stderr, because a
// warm start that never gets a cache is strictly better than a request that
// pays for one.
func (u bytecodeUpload) run(ctx context.Context) {
	budget := bytecodeBudget(ctx)
	if budget <= 0 {
		fmt.Fprintln(os.Stderr, "ocel: no time left to publish the compile cache; skipping")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	ack, ok := u.flush(ctx)
	if !ok {
		return // flushCompileCache already said why
	}
	if !ack.OK {
		fmt.Fprintln(os.Stderr, "ocel: node reported no compile cache to flush; skipping upload")
		return
	}

	version, err := u.nodeVersion(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not read node's version, skipping compile cache upload: %v\n", err)
		return
	}
	nodeVersion, err := canonicalNodeVersion(version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: %v, skipping compile cache upload\n", err)
		return
	}

	size, err := compileCacheSize(ctx, ack.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not measure the compile cache: %v\n", err)
		return
	}
	if size == 0 {
		// The two mean different things: a directory node never created points
		// at the flush, one that exists and holds nothing points at the app.
		if _, err := os.Stat(ack.Dir); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "ocel: node reported a compile cache at %s but nothing is there; skipping upload\n", ack.Dir)
		} else {
			fmt.Fprintf(os.Stderr, "ocel: the compile cache at %s is empty; nothing to upload\n", ack.Dir)
		}
		return
	}
	if exceedsBytecodeCacheCeiling(size) {
		fmt.Fprintf(os.Stderr, "ocel: compile cache is %d bytes, over the %d byte ceiling; skipping upload\n",
			size, bytecodeCacheCeiling)
		return
	}

	archive, err := buildArchiveWithin(ctx, ack.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not archive the compile cache: %v\n", err)
		return
	}

	key := bytecodeCacheKey(u.prefix, u.function, nodeVersion, u.arch)
	exists, err := u.store.objectExists(ctx, u.bucket, key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not check for an existing compile cache at %s: %v\n", key, err)
		return
	}
	if exists {
		return
	}
	if err := u.store.putObject(ctx, u.bucket, key, archive); err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not upload the compile cache to %s: %v\n", key, err)
	}
}

// compileCacheSize sums what a compile cache directory holds without reading a
// byte of it, so the ceiling can be enforced before the walk-read-gzip that
// would otherwise do all that work only to throw it away. A directory that does
// not exist sums to zero, matching how buildBytecodeArchive treats it.
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
		total += info.Size()
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
// version it is. It runs off the request path, once per instance at most, so
// the process it costs is paid by an invocation that has already been answered
// — and it is bounded by the upload's context, so a wedged exec cannot outlive
// the budget.
func nodeVersionFromBinary(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, nodeBinaryPath, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// resolveBytecodeUpload builds the upload this deployment is configured for, or
// nil for one that is configured for none. Nil is the off switch every caller
// checks, so an unset prefix, a missing bucket or an AWS config that will not
// load all land in the same place: the membrane simply never tries.
func resolveBytecodeUpload(ctx context.Context, flush func(context.Context) (compileCacheFlushedPayload, bool)) *bytecodeUpload {
	prefix := os.Getenv(bytecodePrefixEnvVar)
	bucket := os.Getenv("OCEL_ISR_BUCKET")
	function := os.Getenv("AWS_LAMBDA_FUNCTION_NAME")
	if prefix == "" || bucket == "" || function == "" {
		return nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: no aws config for the compile cache upload: %v\n", err)
		return nil
	}
	return &bytecodeUpload{
		store:       s3BytecodeStore{client: s3.NewFromConfig(cfg)},
		bucket:      bucket,
		prefix:      prefix,
		function:    function,
		arch:        runtime.GOARCH,
		flush:       flush,
		nodeVersion: nodeVersionFromBinary,
	}
}
