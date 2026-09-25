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

type Client struct {
	HTTP     *http.Client
	Attempts int
	Base     time.Duration
	Ceiling  time.Duration
	Sleep    func(context.Context, time.Duration) error
}

var DefaultClient = Client{
	HTTP:     http.DefaultClient,
	Attempts: 6,
	Base:     500 * time.Millisecond,
	Ceiling:  30 * time.Second,
}

func Fetch(ctx context.Context, req *http.Request) ([]byte, error) {
	return DefaultClient.Fetch(ctx, req)
}

type retryable struct{ err error }

func (r retryable) Error() string { return r.err.Error() }

func (c Client) Fetch(ctx context.Context, req *http.Request) ([]byte, error) {
	if req.Body != nil {
		return nil, fmt.Errorf("%s %s: a request with a body is not retried", req.Method, req.URL.Path)
	}
	var last error
	for attempt := range c.Attempts {
		body, wait, err := c.once(req.WithContext(ctx))
		if err == nil {
			return body, nil
		}
		var again retryable
		if !errors.As(err, &again) {
			return nil, err
		}
		last = again.err
		if attempt == c.Attempts-1 {
			break
		}
		if wait <= 0 {
			wait = c.backoff(attempt)
		}
		if err := c.sleep(ctx, min(wait, c.Ceiling)); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%s %s after %d attempts: %w", req.Method, req.URL.Path, c.Attempts, last)
}

func (c Client) once(req *http.Request) ([]byte, time.Duration, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		var failed *url.Error
		if errors.As(err, &failed) {
			err = failed.Err
		}
		return nil, 0, retryable{err}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, 0, retryable{err}
		}
		return body, 0, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, retryAfter(resp.Header.Get("Retry-After")), retryable{errors.New(resp.Status)}
	default:
		return nil, 0, fmt.Errorf("%s %s: %s", req.Method, req.URL.Path, resp.Status)
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

func (c Client) backoff(attempt int) time.Duration {
	ceiling := min(c.Base<<attempt, c.Ceiling)
	return time.Duration(rand.Int64N(int64(ceiling)) + 1)
}

func (c Client) sleep(ctx context.Context, wait time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, wait)
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
