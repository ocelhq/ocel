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
	"slices"
	"sync"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

const uploadConcurrency = 64

type ArtifactUploader interface {
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

func walkRegularFiles(dir string) ([]string, error) {
	var rels []string
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
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
	slices.Sort(rels)
	return rels, nil
}

func overlayPaths(overlay map[string][]byte) []string {
	rels := make([]string, 0, len(overlay))
	for rel := range overlay {
		rels = append(rels, rel)
	}
	slices.Sort(rels)
	return rels
}

func hashArtifact(dir string, overlay map[string][]byte) (string, error) {
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
	for _, rel := range overlayPaths(overlay) {
		writeLenPrefixed(h, []byte(rel))
		h.Write([]byte{0})
		writeLenPrefixed(h, overlay[rel])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeLenPrefixed(h io.Writer, b []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(b)))
	h.Write(size[:])
	h.Write(b)
}

func functionArtifactPrefix(c naming.Coordinate) string {
	return c.StoragePrefix() + string(naming.KindFunction)
}

func artifactKey(c naming.Coordinate, logicalName, hash string) string {
	c.Kind = naming.KindFunction
	c.Name = logicalName
	return c.FunctionArtifactKey(hash)
}

func zipDir(dir string, overlay map[string][]byte) ([]byte, error) {
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
	for _, rel := range overlayPaths(overlay) {
		w, err := zw.Create(rel)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(overlay[rel]); err != nil {
			return nil, fmt.Errorf("zip artifact %s: %w", dir, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finalize artifact zip %s: %w", dir, err)
	}
	return buf.Bytes(), nil
}

func copyFileInto(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	f.Close()
	return err
}

func uploadArtifact(ctx context.Context, up ArtifactUploader, bucket, key, contentType string, body func() ([]byte, error)) error {
	_, err := up.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return fmt.Errorf("head artifact %s/%s: %w", bucket, key, err)
	}
	data, err := body()
	if err != nil {
		return err
	}
	return putArtifact(ctx, up, bucket, key, contentType, data)
}

func putArtifact(ctx context.Context, up ArtifactUploader, bucket, key, contentType string, data []byte) error {
	in := &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	if _, err := up.PutObject(ctx, in); err != nil {
		return fmt.Errorf("upload artifact %s/%s: %w", bucket, key, err)
	}
	return nil
}

func isNotFound(err error) bool {
	var nf *s3types.NotFound
	var nsk *s3types.NoSuchKey
	return errors.As(err, &nf) || errors.As(err, &nsk)
}

type artifactRef struct {
	Bucket string
	Key    string
}

func uploadFunctionArtifacts(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, baked map[string]appBundle, builds appBuilds, progress Progress) (map[string]artifactRef, error) {
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

	total := uint32(len(functions))
	var done atomic.Uint32
	progress.report(deploymentsv1.Phase_PHASE_UPLOADING, "Uploading function artifacts", 0, total)

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(uploadConcurrency)

	var mu sync.Mutex
	for _, fn := range functions {
		g.Go(func() error {
			coord, ok := builds.coords[fn.GetApp()]
			if !ok {
				return fmt.Errorf("function %s names the app %q, which this manifest does not declare", fn.GetLogicalName(), fn.GetApp())
			}
			dir := artifactArchivePath(cfg.ArtifactRoot, fn.GetArtifactPath())
			overlay := baked[fn.GetApp()].overlay()
			hash, err := hashArtifact(dir, overlay)
			if err != nil {
				return err
			}
			key := artifactKey(coord, fn.GetLogicalName(), hash)
			if err := uploadArtifact(ctx, cfg.Uploader, cfg.ArtifactBucket, key, "", func() ([]byte, error) {
				return zipDir(dir, overlay)
			}); err != nil {
				return err
			}
			mu.Lock()
			refs[fn.GetLogicalName()] = artifactRef{Bucket: cfg.ArtifactBucket, Key: key}
			progress.report(deploymentsv1.Phase_PHASE_UPLOADING, "Uploading function artifacts", done.Add(1), total)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return refs, nil
}
