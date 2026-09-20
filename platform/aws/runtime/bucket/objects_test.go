package bucket

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/ocelhq/ocel/pkg/constants"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

type storedObject struct {
	body        []byte
	etag        string
	contentType string
	modified    time.Time
	metadata    map[string]string
}

type fakeS3 struct {
	objects map[string]map[string]storedObject
	uploads map[string][][]byte
	nextID  int
	calls   []string
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string]map[string]storedObject{}, uploads: map[string][][]byte{}}
}

func (f *fakeS3) seed(bucket, key string, body string, contentType string) {
	if f.objects[bucket] == nil {
		f.objects[bucket] = map[string]storedObject{}
	}
	f.objects[bucket][key] = storedObject{
		body:        []byte(body),
		etag:        `"` + key + `"`,
		contentType: contentType,
		modified:    time.Unix(1_700_000_000, 0).UTC(),
		metadata:    map[string]string{"owner": "u1"},
	}
}

func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.calls = append(f.calls, "HeadObject")
	obj, ok := f.objects[aws.ToString(in.Bucket)][aws.ToString(in.Key)]
	if !ok {
		return nil, &s3types.NotFound{}
	}
	return &s3.HeadObjectOutput{
		ContentLength: aws.Int64(int64(len(obj.body))),
		ETag:          aws.String(obj.etag),
		ContentType:   aws.String(obj.contentType),
		LastModified:  aws.Time(obj.modified),
		Metadata:      obj.metadata,
	}, nil
}

func (f *fakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.calls = append(f.calls, "ListObjectsV2")
	var keys []string
	for key := range f.objects[aws.ToString(in.Bucket)] {
		if strings.HasPrefix(key, aws.ToString(in.Prefix)) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if after := aws.ToString(in.ContinuationToken); after != "" {
		for len(keys) > 0 && keys[0] <= after {
			keys = keys[1:]
		}
	}
	out := &s3.ListObjectsV2Output{}
	limit := int(aws.ToInt32(in.MaxKeys))
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
		out.IsTruncated = aws.Bool(true)
		out.NextContinuationToken = aws.String(keys[len(keys)-1])
	}
	for _, key := range keys {
		obj := f.objects[aws.ToString(in.Bucket)][key]
		out.Contents = append(out.Contents, s3types.Object{
			Key:          aws.String(key),
			Size:         aws.Int64(int64(len(obj.body))),
			ETag:         aws.String(obj.etag),
			LastModified: aws.Time(obj.modified),
		})
	}
	return out, nil
}

func (f *fakeS3) DeleteObjects(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	f.calls = append(f.calls, "DeleteObjects")
	for _, id := range in.Delete.Objects {
		delete(f.objects[aws.ToString(in.Bucket)], aws.ToString(id.Key))
	}
	return &s3.DeleteObjectsOutput{}, nil
}

func (f *fakeS3) CopyObject(_ context.Context, in *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	f.calls = append(f.calls, "CopyObject")
	source, err := url.PathUnescape(aws.ToString(in.CopySource))
	if err != nil {
		return nil, err
	}
	_, src, _ := strings.Cut(strings.TrimPrefix(source, "/"), "/")
	obj, ok := f.objects[aws.ToString(in.Bucket)][src]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}
	f.objects[aws.ToString(in.Bucket)][aws.ToString(in.Key)] = obj
	return &s3.CopyObjectOutput{}, nil
}

func (f *fakeS3) CreateMultipartUpload(_ context.Context, in *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	f.calls = append(f.calls, "CreateMultipartUpload")
	f.nextID++
	id := "upload-" + strings.Repeat("x", f.nextID)
	f.uploads[id] = nil
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String(id)}, nil
}

func (f *fakeS3) CompleteMultipartUpload(_ context.Context, in *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	f.calls = append(f.calls, "CompleteMultipartUpload")
	if _, ok := f.uploads[aws.ToString(in.UploadId)]; !ok {
		return nil, &s3types.NoSuchUpload{}
	}
	delete(f.uploads, aws.ToString(in.UploadId))
	f.seed(aws.ToString(in.Bucket), aws.ToString(in.Key), "assembled", "application/octet-stream")
	return &s3.CompleteMultipartUploadOutput{ETag: aws.String(`"assembled"`)}, nil
}

func (f *fakeS3) AbortMultipartUpload(_ context.Context, in *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	f.calls = append(f.calls, "AbortMultipartUpload")
	delete(f.uploads, aws.ToString(in.UploadId))
	return &s3.AbortMultipartUploadOutput{}, nil
}

func newObjectService(t *testing.T, objects *fakeS3) *Service {
	t.Helper()
	svc := New(Config{
		DDB:              newFakeDDB(),
		Presigner:        &fakePresigner{},
		Objects:          objects,
		Table:            "sessions",
		SessionKeyPrefix: testSessionKeyPrefix,
		Granted:          func() []string { return []string{"storage", "b"} },
	})
	svc.now = func() time.Time { return time.Unix(1_000_000, 0) }
	return svc
}

func TestHead(t *testing.T) {
	t.Parallel()

	t.Run("an object that is there comes back with its size, etag and type", func(t *testing.T) {
		t.Parallel()
		objects := newFakeS3()
		objects.seed("storage", "avatars/a.png", "0123456789", "image/png")
		svc := newObjectService(t, objects)

		resp, err := svc.Head(context.Background(), &bucketv1.HeadRequest{Bucket: "storage", Key: "avatars/a.png"})
		if err != nil {
			t.Fatalf("Head: %v", err)
		}
		got := resp.GetObject()
		if got.GetKey() != "avatars/a.png" || got.GetSize() != 10 || got.GetContentType() != "image/png" {
			t.Fatalf("Head = %+v, want the seeded object's key, size and type", got)
		}
		if got.GetEtag() != `"avatars/a.png"` {
			t.Fatalf("etag = %q, want the store's etag passed through untouched", got.GetEtag())
		}
		if got.GetMetadata()["owner"] != "u1" {
			t.Fatalf("metadata = %v, want the user metadata the object carries", got.GetMetadata())
		}
		if got.GetUploadedAt().AsTime().Unix() != 1_700_000_000 {
			t.Fatalf("uploaded_at = %v, want the object's last-modified time", got.GetUploadedAt().AsTime())
		}
	})

	t.Run("an object that is not there is no object, not an error", func(t *testing.T) {
		t.Parallel()
		svc := newObjectService(t, newFakeS3())

		resp, err := svc.Head(context.Background(), &bucketv1.HeadRequest{Bucket: "storage", Key: "missing.png"})
		if err != nil {
			t.Fatalf("Head on a missing key = %v, want it answered with no object", err)
		}
		if resp.GetObject() != nil {
			t.Fatalf("Head on a missing key = %+v, want no object", resp.GetObject())
		}
	})
}

func TestList(t *testing.T) {
	t.Parallel()

	seeded := func() *fakeS3 {
		objects := newFakeS3()
		objects.seed("storage", "avatars/a.png", "aa", "image/png")
		objects.seed("storage", "avatars/b.png", "bbb", "image/png")
		objects.seed("storage", "docs/c.pdf", "cccc", "application/pdf")
		return objects
	}

	t.Run("a prefix bounds the listing", func(t *testing.T) {
		t.Parallel()
		svc := newObjectService(t, seeded())

		resp, err := svc.List(context.Background(), &bucketv1.ListRequest{Bucket: "storage", Prefix: "avatars/"})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		var keys []string
		for _, obj := range resp.GetObjects() {
			keys = append(keys, obj.GetKey())
		}
		if len(keys) != 2 || keys[0] != "avatars/a.png" || keys[1] != "avatars/b.png" {
			t.Fatalf("List(avatars/) = %v, want only the objects under that prefix", keys)
		}
	})

	t.Run("a page hands back the cursor that continues it", func(t *testing.T) {
		t.Parallel()
		svc := newObjectService(t, seeded())

		first, err := svc.List(context.Background(), &bucketv1.ListRequest{Bucket: "storage", Limit: 1})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(first.GetObjects()) != 1 || first.GetNextCursor() == "" {
			t.Fatalf("first page = %+v, want one object and a cursor", first)
		}
		second, err := svc.List(context.Background(), &bucketv1.ListRequest{Bucket: "storage", Limit: 1, Cursor: first.GetNextCursor()})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(second.GetObjects()) != 1 || second.GetObjects()[0].GetKey() == first.GetObjects()[0].GetKey() {
			t.Fatalf("second page = %+v, want the object after the first", second)
		}
	})

	t.Run("the reserved prefix is never listed", func(t *testing.T) {
		t.Parallel()
		objects := seeded()
		objects.seed("storage", constants.ReservedKeyPrefix+"sessions/sess_1", "{}", "application/json")
		svc := newObjectService(t, objects)

		resp, err := svc.List(context.Background(), &bucketv1.ListRequest{Bucket: "storage"})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, obj := range resp.GetObjects() {
			if strings.HasPrefix(obj.GetKey(), constants.ReservedKeyPrefix) {
				t.Fatalf("List returned %q, and the store's own bookkeeping is not the app's to see", obj.GetKey())
			}
		}
	})
}

func TestDelete(t *testing.T) {
	t.Parallel()

	t.Run("the named keys go, in one call", func(t *testing.T) {
		t.Parallel()
		objects := newFakeS3()
		objects.seed("storage", "a.png", "a", "image/png")
		objects.seed("storage", "b.png", "b", "image/png")
		objects.seed("storage", "c.png", "c", "image/png")
		svc := newObjectService(t, objects)

		if _, err := svc.Delete(context.Background(), &bucketv1.DeleteRequest{Bucket: "storage", Keys: []string{"a.png", "b.png"}}); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, ok := objects.objects["storage"]["a.png"]; ok {
			t.Fatal("a.png survived the delete")
		}
		if _, ok := objects.objects["storage"]["c.png"]; !ok {
			t.Fatal("c.png was deleted and nobody asked for it")
		}
		deletes := 0
		for _, call := range objects.calls {
			if call == "DeleteObjects" {
				deletes++
			}
		}
		if deletes != 1 {
			t.Fatalf("two keys cost %d DeleteObjects calls, want the one batch the api offers", deletes)
		}
	})

	t.Run("a key that is not there is a success", func(t *testing.T) {
		t.Parallel()
		svc := newObjectService(t, newFakeS3())

		if _, err := svc.Delete(context.Background(), &bucketv1.DeleteRequest{Bucket: "storage", Keys: []string{"gone.png"}}); err != nil {
			t.Fatalf("Delete of a missing key = %v, want silence", err)
		}
	})
}

func TestCopy(t *testing.T) {
	t.Parallel()

	t.Run("the destination carries the source's bytes", func(t *testing.T) {
		t.Parallel()
		objects := newFakeS3()
		objects.seed("storage", "a.png", "0123456789", "image/png")
		svc := newObjectService(t, objects)

		resp, err := svc.Copy(context.Background(), &bucketv1.CopyRequest{Bucket: "storage", SourceKey: "a.png", DestinationKey: "b.png"})
		if err != nil {
			t.Fatalf("Copy: %v", err)
		}
		if resp.GetObject().GetKey() != "b.png" || resp.GetObject().GetSize() != 10 {
			t.Fatalf("Copy = %+v, want the destination object", resp.GetObject())
		}
	})

	t.Run("a source that is not there is not found", func(t *testing.T) {
		t.Parallel()
		svc := newObjectService(t, newFakeS3())

		_, err := svc.Copy(context.Background(), &bucketv1.CopyRequest{Bucket: "storage", SourceKey: "gone.png", DestinationKey: "b.png"})

		var connectErr *connect.Error
		if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeNotFound {
			t.Fatalf("Copy of a missing source err = %v, want CodeNotFound", err)
		}
	})
}
