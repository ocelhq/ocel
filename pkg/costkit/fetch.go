package costkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Fetcher struct {
	Client   *http.Client
	Attempts int
	Base     time.Duration
	Ceiling  time.Duration
	Sleep    func(context.Context, time.Duration) error
}

var DefaultFetcher = Fetcher{
	Client:   http.DefaultClient,
	Attempts: 6,
	Base:     500 * time.Millisecond,
	Ceiling:  30 * time.Second,
}

func Fetch(ctx context.Context, req *http.Request) ([]byte, error) {
	return DefaultFetcher.Fetch(ctx, req)
}

func Stream(ctx context.Context, req *http.Request, consume func(io.Reader) error) error {
	return DefaultFetcher.Stream(ctx, req, consume)
}

type retryable struct{ err error }

func (r retryable) Error() string { return r.err.Error() }

func (f Fetcher) Fetch(ctx context.Context, req *http.Request) ([]byte, error) {
	var body []byte
	err := f.Stream(ctx, req, func(r io.Reader) error {
		read, err := io.ReadAll(r)
		if err != nil {
			return retryable{err}
		}
		body = read
		return nil
	})
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (f Fetcher) Stream(ctx context.Context, req *http.Request, consume func(io.Reader) error) error {
	if req.Body != nil {
		return fmt.Errorf("%s %s: a request with a body is not retried", req.Method, req.URL.Path)
	}
	var last error
	for attempt := range f.Attempts {
		wait, err := f.once(req.WithContext(ctx), consume)
		if err == nil {
			return nil
		}
		var again retryable
		if !errors.As(err, &again) {
			return err
		}
		last = again.err
		if attempt == f.Attempts-1 {
			break
		}
		if wait <= 0 {
			wait = f.backoff(attempt)
		}
		if err := f.sleep(ctx, min(wait, f.Ceiling)); err != nil {
			return err
		}
	}
	return fmt.Errorf("%s %s after %d attempts: %w", req.Method, req.URL.Path, f.Attempts, last)
}

func (f Fetcher) once(req *http.Request, consume func(io.Reader) error) (time.Duration, error) {
	resp, err := f.Client.Do(req)
	if err != nil {
		var failed *url.Error
		if errors.As(err, &failed) {
			err = failed.Err
		}
		return 0, retryable{err}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return 0, consume(resp.Body)
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return retryAfter(resp.Header.Get("Retry-After")), retryable{errors.New(resp.Status)}
	default:
		return 0, fmt.Errorf("%s %s: %s", req.Method, req.URL.Path, resp.Status)
	}
}

func retryAfter(header string) time.Duration {
	if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil {
		return time.Until(at)
	}
	return 0
}

func (f Fetcher) backoff(attempt int) time.Duration {
	ceiling := min(f.Base<<attempt, f.Ceiling)
	return time.Duration(rand.Int64N(int64(ceiling)) + 1)
}

func (f Fetcher) sleep(ctx context.Context, wait time.Duration) error {
	if f.Sleep != nil {
		return f.Sleep(ctx, wait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
