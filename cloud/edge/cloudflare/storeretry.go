package cloudflare

import (
	"context"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

const (
	storeMaxAttempts   = 5
	storeMaxRetryDelay = 8 * time.Second
	storeMaxRetryAfter = time.Minute
)

func storeRetryable(res *http.Response, err error) bool {
	if res == nil {
		return err != nil
	}
	return res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= http.StatusInternalServerError
}

func storeRetryDelay(res *http.Response, attempt int, jitter float64) time.Duration {
	if res != nil {
		if asked, ok := parseStoreRetryAfter(res.Header, time.Now()); ok && asked < storeMaxRetryAfter {
			return asked
		}
	}
	delay := time.Duration(0.5 * float64(time.Second) * math.Pow(2, float64(attempt)))
	if delay > storeMaxRetryDelay {
		delay = storeMaxRetryDelay
	}
	return delay - time.Duration(jitter*float64(delay/4))
}

func parseStoreRetryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	if ms := header.Get("Retry-After-Ms"); ms != "" {
		if parsed, err := strconv.ParseFloat(ms, 64); err == nil && parsed >= 0 {
			return time.Duration(parsed * float64(time.Millisecond)), true
		}
	}
	value := header.Get("Retry-After")
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds * float64(time.Second)), true
	}
	at, err := time.Parse(time.RFC1123, value)
	if err != nil {
		return 0, false
	}
	if wait := at.Sub(now); wait >= 0 {
		return wait, true
	}
	return 0, false
}

func waitBeforeRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryJitter() float64 { return rand.Float64() }
