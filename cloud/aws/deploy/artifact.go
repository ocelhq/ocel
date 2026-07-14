package deploy

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"golang.org/x/sync/errgroup"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// ArtifactUploader is the subset of the S3 client the artifact upload path
// needs: a presence check and a put. The aws-sdk-go-v2 S3 client satisfies it;
// tests substitute a fake.
type ArtifactUploader interface {
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// walkRegularFiles returns every regular file under dir as a sorted slice of
// slash-separated paths relative to dir. Both hashing and zipping consume it,
// so they see the identical, deterministically-ordered file set.
func walkRegularFiles(dir string) ([]string, error) {
	var rels []string
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Regular files and symlinks only; symlinks carry pnpm's node_modules
		// structure and must survive into the package.
		if !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk artifact %s: %w", dir, err)
	}
	sort.Strings(rels)
	return rels, nil
}

// hashArtifact computes a deterministic content hash over a `.func` source
// tree: the sorted set of regular files, each folded in as its relative path,
// executable bit, and contents. It is independent of zip encoding (mod times,
// entry order), so the same source always hashes identically — which is what
// makes the content-addressed S3 key dedup across deploys. The hash changes iff
// a file's path, contents, or executable bit changes.
func hashArtifact(dir string) (string, error) {
	rels, err := walkRegularFiles(dir)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, rel := range rels {
		full := filepath.Join(dir, rel)
		info, err := os.Lstat(full)
		if err != nil {
			return "", err
		}
		// Length-prefix the path so no two distinct (path, contents) layouts can
		// collide by concatenation. The tag byte after it is 0/1 for a regular
		// file's executable bit, 2 for a symlink whose target follows.
		writeLenPrefixed(h, []byte(rel))

		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return "", err
			}
			h.Write([]byte{2})
			writeLenPrefixed(h, []byte(target))
			continue
		}

		var execBit [1]byte
		if info.Mode()&0o100 != 0 {
			execBit[0] = 1
		}
		h.Write(execBit[:])

		f, err := os.Open(full)
		if err != nil {
			return "", err
		}
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(info.Size()))
		h.Write(size[:])
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeLenPrefixed(h io.Writer, b []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(b)))
	h.Write(size[:])
	h.Write(b)
}

// artifactKey is the content-addressed S3 key a function's artifact lives at:
// structured by project then function for human-navigability, keyed by the
// source hash so a code change lands at a new key (and Pulumi redeploys) while
// identical code dedups onto the same object.
func artifactKey(projectID, logicalName, hash string) string {
	return fmt.Sprintf("%s/%s/%s.zip", projectID, logicalName, hash)
}

// zipDir archives a `.func` source tree into an in-memory Lambda deployment
// package: every regular file at its relative path, preserving the executable
// bit. It mirrors what pulumi.NewFileArchive produced, so the packaged Lambda
// is byte-for-byte equivalent in layout.
func zipDir(dir string) ([]byte, error) {
	rels, err := walkRegularFiles(dir)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, rel := range rels {
		full := filepath.Join(dir, rel)
		info, err := os.Lstat(full)
		if err != nil {
			return nil, err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return nil, err
		}
		header.Name = rel
		header.Method = zip.Deflate
		w, err := zw.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		// FileInfoHeader carries the symlink mode; a symlink entry's body is its
		// target path, which unzip on Lambda recreates as a link.
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return nil, err
			}
			if _, err := io.WriteString(w, target); err != nil {
				return nil, fmt.Errorf("zip artifact %s: %w", dir, err)
			}
			continue
		}
		if err := copyFileInto(w, full); err != nil {
			return nil, fmt.Errorf("zip artifact %s: %w", dir, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finalize artifact zip %s: %w", dir, err)
	}
	return buf.Bytes(), nil
}

// copyFileInto streams a file's contents into w, closing the file promptly
// (rather than a deferred close accumulating across a loop).
func copyFileInto(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	f.Close()
	return err
}

// uploadArtifact ensures the object at bucket/key exists, skip-if-exists: a
// present object (identical content already uploaded) is left as-is, a missing
// one (never uploaded, or reaped by the lifecycle rule) is put. body is only
// invoked on a miss, so the caller's zip is not paid when the object is already
// present. A HeadObject error other than "not found" aborts rather than masking
// an outage as "missing".
func uploadArtifact(ctx context.Context, up ArtifactUploader, bucket, key string, body func() ([]byte, error)) error {
	_, err := up.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return fmt.Errorf("head artifact %s: %w", key, err)
	}
	data, err := body()
	if err != nil {
		return err
	}
	if _, err := up.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	}); err != nil {
		return fmt.Errorf("upload artifact %s: %w", key, err)
	}
	return nil
}

// isNotFound reports whether an S3 error is a missing-object result (the two
// shapes HeadObject returns for an absent key).
func isNotFound(err error) bool {
	var nf *s3types.NotFound
	var nsk *s3types.NoSuchKey
	return errors.As(err, &nf) || errors.As(err, &nsk)
}

// artifactRef is where a function's uploaded artifact lives, threaded from the
// pre-up upload pass into the Pulumi program.
type artifactRef struct {
	Bucket string
	Key    string
}

// uploadFunctionArtifacts hashes, zips, and uploads every function's `.func`
// tree to the artifact bucket before provisioning, returning each function's
// artifact reference keyed by logical name. It runs before the Pulumi program
// so the content-addressed objects the Lambdas point at already exist. Uploads
// are skip-if-exists, and the (potentially large) zip is deferred behind that
// presence check, so an unchanged function re-deploying is a cheap HeadObject.
func uploadFunctionArtifacts(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest) (map[string]artifactRef, error) {
	functions := manifest.GetFunctions()
	refs := make(map[string]artifactRef, len(functions))
	if len(functions) == 0 {
		return refs, nil
	}
	if cfg.ArtifactBucket == "" {
		return nil, fmt.Errorf("no artifact bucket configured; re-run `ocel bootstrap`")
	}
	if cfg.Uploader == nil {
		return nil, fmt.Errorf("no artifact uploader configured")
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(8) // tune: bounded S3 conns + bounded peak memory

	var mu sync.Mutex
	for _, fn := range functions {
		g.Go(func() error {
			dir := artifactArchivePath(cfg.ArtifactRoot, fn.GetArtifactPath())
			hash, err := hashArtifact(dir)
			if err != nil {
				return err
			}
			key := artifactKey(manifest.GetProjectId(), fn.GetLogicalName(), hash)
			if err := uploadArtifact(ctx, cfg.Uploader, cfg.ArtifactBucket, key, func() ([]byte, error) {
				return zipDir(dir)
			}); err != nil {
				return err
			}
			mu.Lock()
			refs[fn.GetLogicalName()] = artifactRef{Bucket: cfg.ArtifactBucket, Key: key}
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return refs, nil
}
