package bytecode

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
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
)

const CacheCeiling = 64 << 20

func cacheKey(prefix, functionName, nodeVersion, goArch string) string {
	return fmt.Sprintf("%s/%s/node%s-%s.tar.gz", prefix, functionName, nodeVersion, s3Arch(goArch))
}

func s3Arch(goArch string) string {
	if goArch == "amd64" {
		return "x86_64"
	}
	return goArch
}

var nodeVersionPattern = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)$`)

func canonicalNodeVersion(version string) (string, error) {
	m := nodeVersionPattern.FindStringSubmatch(version)
	if m == nil {
		return "", fmt.Errorf("not a node version: %q", version)
	}
	return m[1], nil
}

func exceedsCeiling(uncompressedSize int64) bool {
	return uncompressedSize > CacheCeiling
}

func buildArchive(ctx context.Context, dir string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

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

const compileCacheDir = "/tmp/" + constants.ProjectStateDirName + "/compile-cache"

const prefixEnvVar = "OCEL_BYTECODE_PREFIX"

const bucketEnvVar = "OCEL_BYTECODE_BUCKET"

const UploadBudget = 2 * time.Second

const rehydrateBudget = 2 * time.Second

const resolveBudget = 2 * time.Second

func Env() []string {
	if os.Getenv(prefixEnvVar) == "" {
		return nil
	}
	return []string{"NODE_COMPILE_CACHE=" + compileCacheDir}
}

type objectStore interface {
	objectExists(ctx context.Context, bucket, key string) (bool, error)
	putObject(ctx context.Context, bucket, key string, body []byte) error
	getObject(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error)
}

var errCacheMiss = errors.New("bytecode cache: no object at that key")

type s3Store struct{ client *s3.Client }

func (s s3Store) objectExists(ctx context.Context, bucket, key string) (bool, error) {
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

func isS3Forbidden(err error) bool {
	var resp *awshttp.ResponseError
	return errors.As(err, &resp) && resp.HTTPStatusCode() == http.StatusForbidden
}

func (s s3Store) putObject(ctx context.Context, bucket, key string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &bucket,
		Key:    &key,
		Body:   bytes.NewReader(body),
	})
	return err
}

func (s s3Store) getObject(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		var noSuchKey *s3types.NoSuchKey
		var notFound *s3types.NotFound
		if errors.As(err, &noSuchKey) || errors.As(err, &notFound) {
			return nil, 0, errCacheMiss
		}
		return nil, 0, err
	}
	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	return out.Body, size, nil
}

type Flushed struct {
	Dir string
	OK  bool
}

type Flush func(ctx context.Context) (Flushed, bool)

type Outcome struct {
	Uploaded bool
	Existed  bool
	Bytes    int64
	Reason   string
}

func abandonUpload(format string, args ...any) Outcome {
	reason := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, "ocel: "+reason)
	return Outcome{Reason: reason}
}

func (c *Cache) Upload(ctx context.Context, by time.Time, flush Flush) Outcome {
	budget := budgetUntil(by)
	if budget <= 0 {
		return abandonUpload("no time left to publish the compile cache; skipping")
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	exists, err := c.store.objectExists(ctx, c.bucket, c.key)
	if err != nil {
		return abandonUpload("could not check for an existing compile cache at %s: %v", c.key, err)
	}
	if exists {
		return Outcome{Existed: true}
	}

	ack, ok := flush(ctx)
	if !ok {
		return Outcome{Reason: "node did not acknowledge the compile-cache flush"}
	}
	if !ack.OK {
		return abandonUpload("node reported no compile cache to flush; skipping upload")
	}

	if !within(c.root, ack.Dir) {
		return abandonUpload("node reported a compile cache at %s, outside %s; skipping upload", ack.Dir, c.root)
	}

	size, err := compileCacheSize(ctx, c.root)
	if err != nil {
		return abandonUpload("could not measure the compile cache: %v", err)
	}
	if size == 0 {
		if _, err := os.Stat(ack.Dir); errors.Is(err, fs.ErrNotExist) {
			return abandonUpload("node reported a compile cache at %s but nothing is there; skipping upload", ack.Dir)
		}
		return abandonUpload("the compile cache at %s is empty; nothing to upload", ack.Dir)
	}
	if exceedsCeiling(size) {
		over := abandonUpload("compile cache is %d bytes, over the %d byte ceiling; skipping upload", size, CacheCeiling)
		over.Bytes = size
		return over
	}

	archive, err := buildArchiveWithin(ctx, c.root)
	if err != nil {
		return abandonUpload("could not archive the compile cache: %v", err)
	}

	if err := c.store.putObject(ctx, c.bucket, c.key, archive); err != nil {
		return abandonUpload("could not upload the compile cache to %s: %v", c.key, err)
	}
	return Outcome{Uploaded: true, Bytes: size}
}

func within(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

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

func buildArchiveWithin(ctx context.Context, dir string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("no budget left to archive %s: %w", dir, err)
	}

	type result struct {
		archive []byte
		err     error
	}
	done := make(chan result, 1)
	go func() {
		archive, err := buildArchive(ctx, dir)
		done <- result{archive: archive, err: err}
	}()
	select {
	case r := <-done:
		return r.archive, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("archiving %s outlasted the upload budget: %w", dir, ctx.Err())
	}
}

const tarEntryOverhead = 512

func untarGzipInto(ctx context.Context, r io.Reader, dir string, ceiling int64) (int64, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, fmt.Errorf("bytecode cache is not gzip: %w", err)
	}
	defer gz.Close()
	return untarInto(ctx, gz, dir, ceiling)
}

func untarInto(ctx context.Context, r io.Reader, dir string, ceiling int64) (int64, error) {
	tr := tar.NewReader(r)

	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, fmt.Errorf("extracting the bytecode cache ran out of time: %w", err)
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
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

func rehydrateCompileCache(ctx context.Context, st objectStore, bucket, key, dir string) (int64, bool) {
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not clear %s before rehydrating the compile cache: %v\n", dir, err)
		return 0, false
	}

	body, size, err := st.getObject(ctx, bucket, key)
	if err != nil {
		if errors.Is(err, errCacheMiss) {
			fmt.Fprintf(os.Stderr, "ocel: no compile cache at %s yet; nothing to rehydrate\n", key)
		} else {
			fmt.Fprintf(os.Stderr, "ocel: could not fetch the compile cache at %s: %v\n", key, err)
		}
		return 0, false
	}
	defer body.Close()

	if exceedsCeiling(size) {
		fmt.Fprintf(os.Stderr, "ocel: compile cache at %s is %d bytes, over the %d byte ceiling; skipping rehydration\n",
			key, size, CacheCeiling)
		return 0, false
	}

	type result struct {
		n   int64
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := untarGzipInto(ctx, body, dir, CacheCeiling)
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
		body.Close()
		<-done
		fmt.Fprintf(os.Stderr, "ocel: rehydrating the compile cache from %s ran out of time: %v\n", key, ctx.Err())
		os.RemoveAll(dir)
		return 0, false
	}
}

type Source string

const (
	SourceEmbedded Source = "embedded"
	SourceS3       Source = "s3"
	SourceNone     Source = "none"
)

const embeddedDir = "/var/task/" + constants.ProjectStateDirName + "/bytecode"

func embeddedPath(key string) string {
	base := path.Base(key)
	if !strings.HasSuffix(base, ".tar.gz") {
		return ""
	}
	return filepath.Join(embeddedDir, strings.TrimSuffix(base, ".gz"))
}

func loadEmbedded(ctx context.Context, tarPath, dir string) (int64, bool) {
	if ctx.Err() != nil {
		return 0, false
	}

	f, err := os.Open(tarPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "ocel: could not open the embedded compile cache at %s: %v\n", tarPath, err)
		}
		return 0, false
	}
	defer f.Close()

	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not clear %s before loading the embedded compile cache at %s: %v\n", dir, tarPath, err)
		return 0, false
	}

	n, err := untarInto(ctx, f, dir, CacheCeiling)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not load the embedded compile cache at %s: %v\n", tarPath, err)
		os.RemoveAll(dir)
		return 0, false
	}
	return n, true
}

func loadEmbeddedCache(ctx context.Context, tarPath, dir string) bool {
	start := time.Now()
	n, hit := loadEmbedded(ctx, tarPath, dir)
	if hit {
		fmt.Fprintf(os.Stderr, "ocel: loaded embedded compile cache from %s: %d bytes in %dms\n",
			tarPath, n, time.Since(start).Milliseconds())
	}
	return hit
}

func budgetUntil(by time.Time) time.Duration {
	if by.IsZero() {
		return UploadBudget
	}
	if remaining := time.Until(by); remaining < UploadBudget {
		return remaining
	}
	return UploadBudget
}

type Cache struct {
	store  objectStore
	bucket string
	key    string
	root   string
	source Source
}

func (c *Cache) Key() string { return c.key }

func (c *Cache) Source() Source {
	if c == nil || c.source == "" {
		return SourceNone
	}
	return c.source
}

func (c *Cache) Cached() bool { return c.Source() != SourceNone }

type Priming struct {
	started   time.Time
	resolved  chan *Cache
	embedded  func(context.Context, *Cache) bool
	rehydrate func(context.Context, *Cache) bool
}

func Start(ctx context.Context, nodeVersion func(context.Context) (string, error)) *Priming {
	p := &Priming{
		started:   time.Now(),
		resolved:  make(chan *Cache, 1),
		embedded:  loadEmbeddedInto,
		rehydrate: rehydrateFromStore,
	}
	go func() {
		resolveCtx, cancel := context.WithTimeout(ctx, resolveBudget)
		defer cancel()
		p.resolved <- resolve(resolveCtx, nodeVersion)
	}()
	return p
}

func (p *Priming) Await(ctx context.Context) *Cache {
	if p == nil {
		return nil
	}
	wait := time.Until(p.started.Add(resolveBudget))
	if wait < 0 {
		wait = 0
	}
	var c *Cache
	select {
	case c = <-p.resolved:
	case <-time.After(wait):
		fmt.Fprintln(os.Stderr, "ocel: compile cache resolution did not finish in time; compile cache disabled")
	}
	if c == nil {
		return nil
	}

	rehydrateCtx, cancel := context.WithTimeout(ctx, rehydrateBudget)
	defer cancel()
	switch {
	case p.embedded(rehydrateCtx, c):
		c.source = SourceEmbedded
	case p.rehydrate(rehydrateCtx, c):
		c.source = SourceS3
	default:
		c.source = SourceNone
	}
	return c
}

func loadEmbeddedInto(ctx context.Context, c *Cache) bool {
	tarPath := embeddedPath(c.key)
	if tarPath == "" {
		return false
	}
	return loadEmbeddedCache(ctx, tarPath, c.root)
}

func resolve(ctx context.Context, nodeVersion func(context.Context) (string, error)) *Cache {
	prefix := os.Getenv(prefixEnvVar)
	bucket := os.Getenv(bucketEnvVar)
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

	cfg, err := sdkconfig.Runtime(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: no aws config for the compile cache: %v\n", err)
		return nil
	}

	return &Cache{
		store:  s3Store{client: s3.NewFromConfig(cfg)},
		bucket: bucket,
		key:    cacheKey(prefix, function, canonical, runtime.GOARCH),
		root:   compileCacheDir,
	}
}

func rehydrateFromStore(ctx context.Context, c *Cache) bool {
	return rehydrateInto(ctx, c, c.root)
}

func rehydrateInto(ctx context.Context, c *Cache, dir string) bool {
	ctx, cancel := context.WithTimeout(ctx, rehydrateBudget)
	defer cancel()

	start := time.Now()
	n, hit := rehydrateCompileCache(ctx, c.store, c.bucket, c.key, dir)
	if hit {
		fmt.Fprintf(os.Stderr, "ocel: rehydrated compile cache from %s: %d bytes in %dms\n",
			c.key, n, time.Since(start).Milliseconds())
	}
	return hit
}
