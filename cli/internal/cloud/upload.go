package cloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/cli/internal/sourcearchive"
	"github.com/ocelhq/ocel/cli/internal/version"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	"github.com/ocelhq/ocel/pkg/proto/cloud/v1/cloudv1connect"
)

const (
	MaxDigestsPerCall = 20000
	putConcurrency    = 8
	attempts          = 5
	firstBackoff      = 500 * time.Millisecond
	maxBackoff        = 30 * time.Second
	maxRetryAfter     = 5 * time.Minute
	callTimeout       = 30 * time.Second
	slowestUpload     = 1 << 20
	maxErrorBody      = 512
)

const VersionHeader = "Ocel-Version"

type TokenSource interface {
	Ensure(ctx context.Context) (string, error)
}

type Client struct {
	apiURL string
	http   *http.Client
	tokens TokenSource
	upload cloudv1connect.UploadServiceClient
	jobs   cloudv1connect.JobServiceClient
	sleep  func(ctx context.Context, d time.Duration) error
}

func New(apiURL string, tokens TokenSource) *Client {
	c := &Client{
		apiURL: strings.TrimRight(apiURL, "/"),
		http:   &http.Client{},
		tokens: tokens,
		sleep:  sleep,
	}
	c.upload = cloudv1connect.NewUploadServiceClient(c.http, c.apiURL, connect.WithInterceptors(c.bearer()))
	c.jobs = cloudv1connect.NewJobServiceClient(c.http, c.apiURL, connect.WithInterceptors(c.bearer()))
	return c
}

func (c *Client) bearer() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			token, err := c.tokens.Ensure(ctx)
			if err != nil {
				return nil, err
			}
			req.Header().Set("Authorization", "Bearer "+token)
			req.Header().Set(VersionHeader, version.Version)
			return next(ctx, req)
		}
	}
}

type Progress struct {
	SentBytes, TotalBytes uint64
	SentFiles, TotalFiles int
}

type Source interface {
	Blobs() []sourcearchive.Blob
	Open(digest string) (io.ReadCloser, error)
}

type Planned struct {
	Missing    []sourcearchive.Blob
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
		for _, digest := range missing.GetDigests() {
			blob, asked := bySum[digest]
			if !asked {
				return Planned{}, fmt.Errorf("%s named %s missing, which this upload never asked about", c.apiURL, digest)
			}
			planned.Missing = append(planned.Missing, blob)
			planned.TotalBytes += blob.Size
		}
	}
	return planned, nil
}

func (c *Client) Send(ctx context.Context, source Source, planned Planned, progress func(Progress)) error {
	tally := &tally{report: progress, at: Progress{TotalBytes: planned.TotalBytes, TotalFiles: len(planned.Missing)}}
	tally.publish()
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(putConcurrency)
	for _, blob := range planned.Missing {
		group.Go(func() error {
			if err := c.put(ctx, source, blob, tally); err != nil {
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

// put sends one blob as PUT {api_url}/v1/blobs/{digest}: the raw bytes as the body and the
// Ocel Cloud token as a bearer. Ocel Cloud hashes the body, refuses one that does not match
// the digest, and answers 2xx once the blob is stored for the caller's tenant, or already was.
func (c *Client) put(ctx context.Context, source Source, blob sourcearchive.Blob, tally *tally) error {
	err := c.retry(ctx, func(ctx context.Context) (asked time.Duration, err error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout+time.Duration(blob.Size/slowestUpload)*time.Second)
		defer cancel()
		token, err := c.tokens.Ensure(ctx)
		if err != nil {
			return 0, err
		}
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
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.apiURL+"/v1/blobs/"+blob.Digest, counted)
		if err != nil {
			return 0, err
		}
		req.ContentLength = int64(blob.Size)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := c.http.Do(req)
		if err != nil {
			return 0, retryable{err}
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
			return 0, nil
		}
		said, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		err = fmt.Errorf("%s answered %s: %s", c.apiURL, resp.Status, strings.TrimSpace(string(said)))
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
