package deploy

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, contents := range withServeDescriptors(t, files) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type fakeUploader struct {
	exists  map[string]bool
	headErr error
	putErr  error

	mu            sync.Mutex
	puts          []string
	buckets       []string
	putBodies     map[string]string
	contentTypes  map[string]string
	cacheControls map[string]string
}

func (f *fakeUploader) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if f.headErr != nil {
		return nil, f.headErr
	}
	if f.exists[aws.ToString(in.Key)] {
		return &s3.HeadObjectOutput{}, nil
	}
	return nil, &s3types.NotFound{}
}

func (f *fakeUploader) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if f.putErr != nil {
		return nil, f.putErr
	}
	key := aws.ToString(in.Key)
	if aws.ToString(in.IfNoneMatch) == "*" && f.exists[key] {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed"}
	}
	var body []byte
	if in.Body != nil {
		body, _ = io.ReadAll(in.Body)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, key)
	f.buckets = append(f.buckets, aws.ToString(in.Bucket))
	if in.ContentType != nil {
		if f.contentTypes == nil {
			f.contentTypes = map[string]string{}
		}
		f.contentTypes[key] = aws.ToString(in.ContentType)
	}
	if in.CacheControl != nil {
		if f.cacheControls == nil {
			f.cacheControls = map[string]string{}
		}
		f.cacheControls[key] = aws.ToString(in.CacheControl)
	}
	if in.Body != nil {
		if f.putBodies == nil {
			f.putBodies = map[string]string{}
		}
		f.putBodies[key] = string(body)
	}
	return &s3.PutObjectOutput{}, nil
}

func bodyFn(called *bool) func() ([]byte, error) {
	return func() ([]byte, error) {
		*called = true
		return []byte("data"), nil
	}
}

func TestUploadArtifact(t *testing.T) {
	t.Run("skips when present", func(t *testing.T) {
		t.Parallel()
		f := &fakeUploader{exists: map[string]bool{"k.zip": true}}
		var zipped bool
		transferred, err := uploadArtifact(context.Background(), f, "bucket", "k.zip", objectHeaders{}, bodyFn(&zipped))
		if err != nil {
			t.Fatalf("uploadArtifact: %v", err)
		}
		if transferred {
			t.Error("transferred = true, want false for a cache hit")
		}
		if len(f.puts) != 0 {
			t.Errorf("PutObject called %d times, want 0 (object already present)", len(f.puts))
		}
		if zipped {
			t.Error("body (zip) was invoked despite the object already being present")
		}
	})

	t.Run("uploads when missing", func(t *testing.T) {
		t.Parallel()
		f := &fakeUploader{exists: map[string]bool{}}
		var zipped bool
		transferred, err := uploadArtifact(context.Background(), f, "bucket", "k.zip", objectHeaders{}, bodyFn(&zipped))
		if err != nil {
			t.Fatalf("uploadArtifact: %v", err)
		}
		if !transferred {
			t.Error("transferred = false, want true for a cache miss")
		}
		if len(f.puts) != 1 || f.puts[0] != "k.zip" {
			t.Errorf("PutObject calls = %v, want single [k.zip]", f.puts)
		}
		if !zipped {
			t.Error("body (zip) was not invoked on a cache miss")
		}
		if ct, ok := f.contentTypes["k.zip"]; ok {
			t.Errorf("content-type = %q, want unset when caller passes \"\"", ct)
		}
	})

	t.Run("sets content type when given", func(t *testing.T) {
		t.Parallel()
		f := &fakeUploader{exists: map[string]bool{}}
		var invoked bool
		if _, err := uploadArtifact(context.Background(), f, "bucket", "app.js", objectHeaders{contentType: "text/javascript; charset=utf-8"}, bodyFn(&invoked)); err != nil {
			t.Fatalf("uploadArtifact: %v", err)
		}
		if got := f.contentTypes["app.js"]; got != "text/javascript; charset=utf-8" {
			t.Errorf("content-type = %q, want %q", got, "text/javascript; charset=utf-8")
		}
	})

	t.Run("head error surfaces", func(t *testing.T) {
		t.Parallel()
		f := &fakeUploader{headErr: errors.New("access denied")}
		var zipped bool
		transferred, err := uploadArtifact(context.Background(), f, "bucket", "k.zip", objectHeaders{}, bodyFn(&zipped))
		if err == nil {
			t.Fatal("uploadArtifact = nil, want the HeadObject error surfaced")
		}
		if transferred {
			t.Error("transferred = true, want false when HeadObject itself fails")
		}
		if len(f.puts) != 0 {
			t.Errorf("PutObject called despite HeadObject error: %v", f.puts)
		}
	})

	t.Run("failures name the bucket", func(t *testing.T) {
		t.Parallel()
		denied := errors.New("AccessDenied")
		for _, bucket := range []string{"r2-cache-store", "s3-asset-bucket"} {
			_, head := uploadArtifact(context.Background(), &fakeUploader{headErr: denied}, bucket, "assets/proj/web/B1/logo.png", objectHeaders{}, bodyFn(new(bool)))
			if head == nil || !strings.Contains(head.Error(), bucket) {
				t.Errorf("head failure = %v, want it to name %q", head, bucket)
			}
			put := putArtifact(context.Background(), &fakeUploader{putErr: denied}, bucket, "assets/proj/web/B1/logo.png", objectHeaders{}, []byte("PNG"))
			if put == nil || !strings.Contains(put.Error(), bucket) {
				t.Errorf("put failure = %v, want it to name %q", put, bucket)
			}
		}
	})
}
