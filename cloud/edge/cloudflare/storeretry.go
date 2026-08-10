package cloudflare

import (
	"context"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// The deployments-store worker is reached over plain HTTP rather than through
// the cloudflare-go client, so it carries its own retry policy — the same shape
// the SDK applies to every API call it makes.
const (
	// storeMaxAttempts is how many times one store call is made in total.
	storeMaxAttempts = 5
	// storeMaxRetryDelay caps the computed backoff, and storeMaxRetryAfter caps
	// what the store may ask for: beyond it the deploy is better served by the
	// cap than by the store's word.
	storeMaxRetryDelay = 8 * time.Second
	storeMaxRetryAfter = time.Minute
)

// storeRetryable reports whether a store call is worth making again: one that
// never got a response at all, one the store throttled, or one it failed to
// serve. A rejected credential, a bad request or a slug collision are answers,
// not failures, and are returned as they stand.
func storeRetryable(res *http.Response, err error) bool {
	if res == nil {
		return err != nil
	}
	return res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= http.StatusInternalServerError
}

// storeRetryDelay is how long to wait after a failed attempt (0-based): what the
// store asked for when that is a reasonable wait, and otherwise an exponential
// backoff under storeMaxRetryDelay, shortened by up to a quarter so concurrent
// deploys that backed off from one throttle do not return together. jitter is
// the fraction of that quarter to take off, in [0,1).
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

// parseStoreRetryAfter reads the wait a response asked for: Retry-After-Ms in
// milliseconds, else Retry-After in seconds or as an HTTP-date. A missing,
// unparseable or already-elapsed value reports false, leaving the caller its own
// backoff.
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

// waitBeforeRetry sleeps for delay unless ctx ends first, in which case it
// reports ctx's error so a cancelled deploy stops retrying immediately.
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

// retryJitter is the random fraction storeRetryDelay shortens a backoff by.
func retryJitter() float64 { return rand.Float64() }
