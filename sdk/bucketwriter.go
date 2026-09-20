package ocel

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

// A Writer streams one object's bytes into a bucket. Small bodies go up in a
// single request; a body that outgrows what one request carries is uploaded in
// parts, which [Writer.Close] settles and [Writer.Abort] throws away. Nothing
// is stored until Close returns without an error.
type Writer struct {
	ctx     context.Context
	store   *BucketStore
	reached *reachedBucket
	key     string
	options writeOptions

	buffered  []byte
	pending   []stagedPart
	completed []*bucketv1.CompletedPart
	uploadID  string
	next      int32

	closed bool
	err    error
}

type stagedPart struct {
	number int32
	data   []byte
}

// NewWriter opens the object under key for writing. Nothing reaches the bucket
// until [Writer.Close] returns without an error.
func (b *BucketStore) NewWriter(ctx context.Context, key string, opts ...WriteOption) *Writer {
	w := &Writer{ctx: ctx, store: b, key: key, next: 1}
	for _, opt := range opts {
		opt.applyWrite(&w.options)
	}
	w.reached, w.err = b.runtime("NewWriter")
	return w
}

// WriteAll writes data as the whole object under key.
func (b *BucketStore) WriteAll(ctx context.Context, key string, data []byte, opts ...WriteOption) error {
	w := b.NewWriter(ctx, key, opts...)
	if _, err := w.Write(data); err != nil {
		w.Abort(err)
		return err
	}
	return w.Close()
}

// Write buffers p, sending a part on its way whenever enough bytes have come
// in to fill one.
func (w *Writer) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.closed {
		return 0, fmt.Errorf("the writer for %q is closed", w.key)
	}
	w.buffered = append(w.buffered, p...)
	if err := w.drain(false); err != nil {
		w.fail(err)
		return 0, err
	}
	return len(p), nil
}

// Close settles the write and reports whether the object reached the bucket. A
// write conditioned with [IfNotExists] or [IfMatch] that the object did not
// meet reports [ErrPreconditionFailed] here.
func (w *Writer) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err != nil {
		w.discard()
		return w.err
	}
	if w.uploadID == "" {
		if err := w.putWhole(); err != nil {
			w.err = err
		}
		return w.err
	}
	if err := w.drain(true); err != nil {
		w.fail(err)
		return w.err
	}
	if err := w.settle(); err != nil {
		w.fail(err)
		return w.err
	}
	return nil
}

// Abort abandons the write, throwing away every part already on its way, and
// makes [Writer.Close] report cause.
func (w *Writer) Abort(cause error) {
	if w.closed {
		return
	}
	w.closed = true
	if cause == nil {
		cause = fmt.Errorf("the write of %q was abandoned", w.key)
	}
	w.err = cause
	w.discard()
}

func (w *Writer) fail(err error) {
	if w.err == nil {
		w.err = err
	}
	w.discard()
}

func (w *Writer) discard() {
	if w.uploadID == "" {
		return
	}
	id := w.uploadID
	w.uploadID = ""
	_, _ = w.reached.client.AbortMultipart(context.WithoutCancel(w.ctx),
		&bucketv1.AbortMultipartRequest{
			Bucket:   w.reached.bucket,
			Key:      w.key,
			UploadId: id,
		})
}

func (w *Writer) drain(final bool) error {
	if w.uploadID == "" {
		if final || int64(len(w.buffered)) <= w.store.singleCeiling {
			return nil
		}
		if err := w.begin(); err != nil {
			return err
		}
	}
	for int64(len(w.buffered)) >= w.store.partSize {
		w.stage(slices.Clone(w.buffered[:w.store.partSize]))
		w.buffered = w.buffered[w.store.partSize:]
		if len(w.pending) == partsInFlight {
			if err := w.flush(); err != nil {
				return err
			}
		}
	}
	if !final {
		return nil
	}
	if len(w.buffered) > 0 {
		w.stage(slices.Clone(w.buffered))
		w.buffered = nil
	}
	return w.flush()
}

func (w *Writer) begin() error {
	res, err := w.reached.client.CreateMultipart(w.ctx, &bucketv1.CreateMultipartRequest{
		Bucket:       w.reached.bucket,
		Key:          w.key,
		ContentType:  w.options.contentType,
		CacheControl: w.options.cacheControl,
		Metadata:     w.options.metadata,
	})
	if err != nil {
		return refused(w.key, err)
	}
	w.uploadID = res.GetUploadId()
	return nil
}

func (w *Writer) stage(data []byte) {
	w.pending = append(w.pending, stagedPart{number: w.next, data: data})
	w.next++
}

func (w *Writer) flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	numbers := make([]int32, 0, len(w.pending))
	for _, part := range w.pending {
		numbers = append(numbers, part.number)
	}
	res, err := w.reached.client.SignParts(w.ctx, &bucketv1.SignPartsRequest{
		Bucket:      w.reached.bucket,
		Key:         w.key,
		UploadId:    w.uploadID,
		PartNumbers: numbers,
		Audience:    bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
	})
	if err != nil {
		return refused(w.key, err)
	}
	targets := map[int32]*bucketv1.SignedPart{}
	for _, target := range res.GetParts() {
		targets[target.GetPartNumber()] = target
	}

	var wait sync.WaitGroup
	var guard sync.Mutex
	var failure error
	for _, part := range w.pending {
		target, signed := targets[part.number]
		if !signed {
			return fmt.Errorf("the runtime signed no url for part %d of %q", part.number, w.key)
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			etag, err := w.send(target, part.data)
			guard.Lock()
			defer guard.Unlock()
			if err != nil {
				if failure == nil {
					failure = err
				}
				return
			}
			w.completed = append(w.completed, &bucketv1.CompletedPart{PartNumber: part.number, Etag: etag})
		}()
	}
	wait.Wait()
	w.pending = nil
	return failure
}

func (w *Writer) send(target *bucketv1.SignedPart, data []byte) (string, error) {
	req, err := http.NewRequestWithContext(w.ctx, http.MethodPut, target.GetUrl(), bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	for name, value := range target.GetHeaders() {
		req.Header.Set(name, value)
	}
	req.ContentLength = int64(len(data))
	res, err := w.store.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("part %d of %q was refused (%d)", target.GetPartNumber(), w.key, res.StatusCode)
	}
	return res.Header.Get("ETag"), nil
}

func (w *Writer) settle() error {
	slices.SortFunc(w.completed, func(a, b *bucketv1.CompletedPart) int {
		return int(a.GetPartNumber() - b.GetPartNumber())
	})
	id := w.uploadID
	_, err := w.reached.client.CompleteMultipart(w.ctx, &bucketv1.CompleteMultipartRequest{
		Bucket:      w.reached.bucket,
		Key:         w.key,
		UploadId:    id,
		Parts:       w.completed,
		IfNoneMatch: w.options.ifNoneMatch,
		IfMatch:     w.options.ifMatch,
	})
	if err != nil {
		return refused(w.key, err)
	}
	w.uploadID = ""
	return nil
}

func (w *Writer) putWhole() error {
	target, err := w.store.sign(w.ctx, "Close", w.key,
		bucketv1.SignedOperation_SIGNED_OPERATION_PUT,
		bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
		&bucketv1.SignConstraints{
			ContentType: w.options.contentType,
			IfNoneMatch: w.options.ifNoneMatch,
			IfMatch:     w.options.ifMatch,
		}, 0,
	)
	if err != nil {
		return err
	}
	method := target.GetMethod()
	if method == "" {
		method = http.MethodPut
	}
	req, err := http.NewRequestWithContext(w.ctx, method, target.GetUrl(), bytes.NewReader(w.buffered))
	if err != nil {
		return err
	}
	for name, value := range target.GetHeaders() {
		req.Header.Set(name, value)
	}
	if w.options.contentType != "" {
		req.Header.Set("Content-Type", w.options.contentType)
	}
	if w.options.cacheControl != "" {
		req.Header.Set("Cache-Control", w.options.cacheControl)
	}
	if w.options.ifNoneMatch != "" {
		req.Header.Set("If-None-Match", w.options.ifNoneMatch)
	}
	if w.options.ifMatch != "" {
		req.Header.Set("If-Match", w.options.ifMatch)
	}
	for name, value := range w.options.metadata {
		req.Header.Set("x-amz-meta-"+name, value)
	}
	req.ContentLength = int64(len(w.buffered))
	res, err := w.store.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode >= http.StatusMultipleChoices {
		if known := refusedStatus(w.key, res.StatusCode); known != nil {
			return known
		}
		return fmt.Errorf("writing %q was refused (%d)", w.key, res.StatusCode)
	}
	return nil
}
