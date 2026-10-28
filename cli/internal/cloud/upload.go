package cloud

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/sourcearchive"
	"github.com/ocelhq/ocel/cli/internal/wireerror"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	"github.com/ocelhq/ocel/pkg/proto/cloud/v1/cloudv1connect"
	"github.com/ocelhq/ocel/pkg/tracing"
)

const (
	MaxDigestsPerCall = 20000
	uploadConcurrency = 8
	attempts          = 5
	firstBackoff      = 500 * time.Millisecond
	maxBackoff        = 30 * time.Second
	maxRetryAfter     = 5 * time.Minute
	callTimeout       = 30 * time.Second
	slowestUpload     = 1 << 20
	maxErrorBody      = 512
)

type Client struct {
	apiURL  string
	http    *http.Client
	headers Headers
	upload  cloudv1connect.UploadServiceClient
	jobs    cloudv1connect.JobServiceClient
	sleep   func(ctx context.Context, d time.Duration) error
}

func New(apiURL string, tokens TokenSource) *Client {
	c := &Client{
		apiURL:  strings.TrimRight(apiURL, "/"),
		http:    tracing.NewClient(0),
		headers: Headers{Tokens: tokens},
		sleep:   sleep,
	}
	interceptors := connect.WithInterceptors(c.headers, wireerror.Interceptor(func(err *connect.Error) *clierror.Error {
		return RefusalOf(c.apiURL, err)
	}))
	c.upload = cloudv1connect.NewUploadServiceClient(c.http, c.apiURL, interceptors)
	c.jobs = cloudv1connect.NewJobServiceClient(c.http, c.apiURL, interceptors)
	return c
}

type Progress struct {
	SentBytes, TotalBytes uint64
	SentFiles, TotalFiles int
}

type Source interface {
	Blobs() []sourcearchive.Blob
	Open(digest string) (io.ReadCloser, error)
}

type Upload struct {
	Blob   sourcearchive.Blob
	Target *cloudv1.Upload
}

type Planned struct {
	Missing    []Upload
	TotalBytes uint64
}

func (c *Client) Plan(ctx context.Context, source Source) (Planned, error) {
	blobs := source.Blobs()
	bySum := make(map[string]sourcearchive.Blob, len(blobs))
	digests := make([]string, 0, len(blobs))
	for _, blob := range blobs {
		bySum[blob.Digest] = blob
		digests = append(digests, blob.Digest)
	}
	var planned Planned
	for start := 0; start < len(digests); start += MaxDigestsPerCall {
		batch := digests[start:min(start+MaxDigestsPerCall, len(digests))]
		var missing *cloudv1.MissingResponse
		err := c.retry(ctx, func(ctx context.Context) (time.Duration, error) {
			ctx, cancel := context.WithTimeout(ctx, callTimeout)
			defer cancel()
			var err error
			missing, err = c.upload.Missing(ctx, &cloudv1.MissingRequest{Digests: batch})
			return 0, retryableCall(err)
		})
		if err != nil {
			return Planned{}, fmt.Errorf("ask Ocel Cloud at %s which source files it lacks: %w", c.apiURL, err)
		}
		if err := protovalidate.Validate(missing); err != nil {
			return Planned{}, fmt.Errorf("%s handed out upload targets that break their rules: %w", c.apiURL, err)
		}
		for _, target := range missing.GetUploads() {
			blob, asked := bySum[target.GetDigest()]
			if !asked {
				return Planned{}, fmt.Errorf("%s named %s missing, which this upload never asked about", c.apiURL, target.GetDigest())
			}
			planned.Missing = append(planned.Missing, Upload{Blob: blob, Target: target})
			planned.TotalBytes += blob.Size
		}
	}
	return planned, nil
}

func (c *Client) Send(ctx context.Context, source Source, planned Planned, progress func(Progress)) error {
	tally := &tally{report: progress, at: Progress{TotalBytes: planned.TotalBytes, TotalFiles: len(planned.Missing)}}
	tally.publish()
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(uploadConcurrency)
	for _, upload := range planned.Missing {
		group.Go(func() error {
			if err := c.send(ctx, source, upload, tally); err != nil {
				return err
			}
			tally.finish()
			return nil
		})
	}
	return group.Wait()
}

func (c *Client) Commit(ctx context.Context, manifest *cloudv1.SourceManifest) (string, error) {
	var committed *cloudv1.CommitSourceResponse
	err := c.retry(ctx, func(ctx context.Context) (time.Duration, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		var err error
		committed, err = c.upload.CommitSource(ctx, &cloudv1.CommitSourceRequest{Manifest: manifest})
		return 0, retryableCall(err)
	})
	if err != nil {
		return "", fmt.Errorf("commit the source to Ocel Cloud at %s: %w", c.apiURL, err)
	}
	return committed.GetSourceId(), nil
}

func (c *Client) send(ctx context.Context, source Source, upload Upload, tally *tally) error {
	blob := upload.Blob
	err := c.retry(ctx, func(ctx context.Context) (asked time.Duration, err error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout+time.Duration(blob.Size/slowestUpload)*time.Second)
		defer cancel()
		body, err := source.Open(blob.Digest)
		if err != nil {
			return 0, err
		}
		defer body.Close()
		counted := &countingReader{r: body, tally: tally}
		defer func() {
			if err != nil {
				tally.unsend(counted.n)
			}
		}()
		req, err := newUploadRequest(ctx, upload, counted)
		if err != nil {
			return 0, err
		}
		where := req.Method + " " + req.URL.Host
		resp, err := c.http.Do(req)
		if err != nil {
			if transfer, ok := errors.AsType[*url.Error](err); ok {
				err = transfer.Err
			}
			return 0, retryable{fmt.Errorf("%s: %w", where, err)}
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
			return 0, nil
		}
		said, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		err = fmt.Errorf("%s answered %s: %s", where, resp.Status, strings.TrimSpace(string(said)))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return retryAfter(resp.Header.Get("Retry-After")), retryable{err}
		}
		return 0, err
	})
	if err != nil {
		return fmt.Errorf("upload the source file %s to Ocel Cloud: %w", blob.Digest, err)
	}
	return nil
}

func newUploadRequest(ctx context.Context, upload Upload, file io.Reader) (*http.Request, error) {
	target := upload.Target
	body, length, contentType := file, int64(upload.Blob.Size), ""
	if target.GetMethod() == http.MethodPost {
		var framing bytes.Buffer
		form := multipart.NewWriter(&framing)
		for _, name := range slices.Sorted(maps.Keys(target.GetFields())) {
			if err := form.WriteField(name, target.GetFields()[name]); err != nil {
				return nil, err
			}
		}
		if _, err := form.CreateFormFile("file", upload.Blob.Digest); err != nil {
			return nil, err
		}
		head := framing.Len()
		if err := form.Close(); err != nil {
			return nil, err
		}
		prefix, suffix := framing.Bytes()[:head], framing.Bytes()[head:]
		body = io.MultiReader(bytes.NewReader(prefix), file, bytes.NewReader(suffix))
		length += int64(len(prefix) + len(suffix))
		contentType = form.FormDataContentType()
	}
	req, err := http.NewRequestWithContext(ctx, target.GetMethod(), target.GetUrl(), body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = length
	for name, value := range target.GetHeaders() {
		req.Header.Set(name, value)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

type retryable struct{ error }

func (r retryable) Unwrap() error { return r.error }

func retryableCall(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeResourceExhausted, connect.CodeDeadlineExceeded:
		return retryable{err}
	}
	return err
}

func (c *Client) retry(ctx context.Context, attempt func(ctx context.Context) (time.Duration, error)) error {
	var last error
	for n := 1; ; n++ {
		asked, err := attempt(ctx)
		if err == nil {
			return nil
		}
		last = err
		var again retryable
		if !errors.As(err, &again) || ctx.Err() != nil {
			return err
		}
		if n == attempts {
			return fmt.Errorf("gave up after %d attempts: %w", attempts, again.error)
		}
		if err := c.sleep(ctx, backoff(n, asked)); err != nil {
			return errors.Join(err, last)
		}
	}
}

func backoff(attempt int, asked time.Duration) time.Duration {
	if asked > 0 {
		return min(asked, maxRetryAfter)
	}
	ceiling := min(firstBackoff<<(attempt-1), maxBackoff)
	return ceiling/2 + rand.N(ceiling/2)
}

func retryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil {
		return max(time.Until(at), 0)
	}
	return 0
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type tally struct {
	report func(Progress)
	mu     sync.Mutex
	at     Progress
}

func (t *tally) publish() {
	if t.report != nil {
		t.report(t.at)
	}
}

func (t *tally) send(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.at.SentBytes += uint64(n)
	t.publish()
}

func (t *tally) unsend(n uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.at.SentBytes -= n
	t.publish()
}

func (t *tally) finish() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.at.SentFiles++
	t.publish()
}

type countingReader struct {
	r     io.Reader
	tally *tally
	n     uint64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += uint64(n)
	c.tally.send(n)
	return n, err
}
