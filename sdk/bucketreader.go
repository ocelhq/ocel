package ocel

import (
	"context"
	"fmt"
	"io"
	"net/http"

	bucketv1 "ocel.dev/internal/proto/app/bucket/v1"
)

// A Reader streams one object's bytes out of a bucket. Close it when the read
// is done, whether or not it reached the end.
type Reader struct {
	body  io.ReadCloser
	attrs *Object
}

// Read fills p with the object's bytes.
func (r *Reader) Read(p []byte) (int, error) { return r.body.Read(p) }

// Close releases the connection the object is streaming over.
func (r *Reader) Close() error { return r.body.Close() }

// Attrs is what the bucket knew about the object when the read opened. It
// describes the whole object, not the range this reader carries.
func (r *Reader) Attrs() *Object { return r.attrs }

// NewReader opens the object under key for reading. It reports
// [ErrObjectNotFound] when the bucket holds none.
func (b *BucketStore) NewReader(ctx context.Context, key string) (*Reader, error) {
	return b.newReader(ctx, "NewReader", key, 0, -1)
}

// NewRangeReader opens length bytes of the object under key, starting at
// offset. A negative length reads to the end. It reports [ErrObjectNotFound]
// when the bucket holds no such object.
func (b *BucketStore) NewRangeReader(ctx context.Context, key string, offset, length int64) (*Reader, error) {
	return b.newReader(ctx, "NewRangeReader", key, offset, length)
}

// ReadAll is every byte of the object under key. It reports
// [ErrObjectNotFound] when the bucket holds none.
func (b *BucketStore) ReadAll(ctx context.Context, key string) ([]byte, error) {
	reader, err := b.NewReader(ctx, key)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func (b *BucketStore) newReader(ctx context.Context, access, key string, offset, length int64) (*Reader, error) {
	attrs, err := b.head(ctx, access, key)
	if err != nil {
		return nil, err
	}
	if attrs == nil {
		return nil, objectNotFound(key)
	}
	target, err := b.sign(ctx, access, key,
		bucketv1.SignedOperation_SIGNED_OPERATION_GET,
		bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
		nil, 0,
	)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.GetUrl(), nil)
	if err != nil {
		return nil, err
	}
	for name, value := range target.GetHeaders() {
		req.Header.Set(name, value)
	}
	if offset > 0 || length >= 0 {
		req.Header.Set("Range", byteRange(offset, length))
	}
	res, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= http.StatusMultipleChoices {
		res.Body.Close()
		if known := refusedStatus(key, res.StatusCode); known != nil {
			return nil, known
		}
		return nil, fmt.Errorf("reading %q was refused (%d)", key, res.StatusCode)
	}
	return &Reader{body: res.Body, attrs: attrs}, nil
}

func byteRange(offset, length int64) string {
	if length < 0 {
		return fmt.Sprintf("bytes=%d-", offset)
	}
	return fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
}
