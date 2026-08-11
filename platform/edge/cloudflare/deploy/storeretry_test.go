package cloudflare

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestStoreRetryable(t *testing.T) {
	t.Parallel()

	t.Run("a request that never reached the store retries", func(t *testing.T) {
		t.Parallel()

		if !storeRetryable(nil, errors.New("connection refused")) {
			t.Error("a request that never reached the store must retry")
		}
	})

	for _, tc := range []struct {
		name   string
		status int
		want   bool
	}{
		{"a success is final", http.StatusOK, false},
		{"a rejected credential is final", http.StatusUnauthorized, false},
		{"a conflict is final", http.StatusConflict, false},
		{"a missing instance is final", http.StatusNotFound, false},
		{"a rate limit retries", http.StatusTooManyRequests, true},
		{"an internal error retries", http.StatusInternalServerError, true},
		{"a bad gateway retries", http.StatusBadGateway, true},
		{"an unavailable store retries", http.StatusServiceUnavailable, true},
		{"a gateway timeout retries", http.StatusGatewayTimeout, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res := &http.Response{StatusCode: tc.status, Header: http.Header{}}
			if got := storeRetryable(res, nil); got != tc.want {
				t.Errorf("storeRetryable(%d) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

func TestParseStoreRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name   string
		header map[string]string
		want   time.Duration
		wantOK bool
	}{
		{
			name:   "milliseconds win over whole seconds",
			header: map[string]string{"Retry-After-Ms": "250", "Retry-After": "30"},
			want:   250 * time.Millisecond,
			wantOK: true,
		},
		{
			name:   "the seconds form is honored",
			header: map[string]string{"Retry-After": "2"},
			want:   2 * time.Second,
			wantOK: true,
		},
		{
			name:   "the HTTP-date form is honored as a wait from now",
			header: map[string]string{"Retry-After": now.Add(3 * time.Second).Format(time.RFC1123)},
			want:   3 * time.Second,
			wantOK: true,
		},
		{"no header asks for nothing", nil, 0, false},
		{"an unparsable value is not honored", map[string]string{"Retry-After": "soon"}, 0, false},
		{"a negative wait is not honored", map[string]string{"Retry-After": "-1"}, 0, false},
		{
			name:   "an HTTP-date already past is not honored",
			header: map[string]string{"Retry-After": now.Add(-time.Second).Format(time.RFC1123)},
			wantOK: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := http.Header{}
			for k, v := range tc.header {
				h.Set(k, v)
			}
			got, ok := parseStoreRetryAfter(h, now)
			if ok != tc.wantOK || (tc.wantOK && got != tc.want) {
				t.Errorf("parseStoreRetryAfter = %v, %v; want %v, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestStoreRetryDelay(t *testing.T) {
	t.Parallel()

	t.Run("an unreasonable Retry-After is ignored for the capped backoff", func(t *testing.T) {
		t.Parallel()

		h := http.Header{}
		h.Set("Retry-After", "600")
		res := &http.Response{StatusCode: http.StatusTooManyRequests, Header: h}

		if got := storeRetryDelay(res, 0, 0); got > storeMaxRetryDelay {
			t.Errorf("delay = %v, want at most the capped backoff %v", got, storeMaxRetryDelay)
		}
	})

	t.Run("a reasonable Retry-After wins over attempt and jitter", func(t *testing.T) {
		t.Parallel()

		h := http.Header{}
		h.Set("Retry-After-Ms", "1500")
		res := &http.Response{StatusCode: http.StatusServiceUnavailable, Header: h}

		if got := storeRetryDelay(res, 3, 0.9); got != 1500*time.Millisecond {
			t.Errorf("delay = %v, want the requested 1.5s regardless of attempt or jitter", got)
		}
	})

	t.Run("backoff grows exponentially and never passes the cap", func(t *testing.T) {
		t.Parallel()

		var prev time.Duration
		for attempt := range 8 {
			got := storeRetryDelay(nil, attempt, 0)
			if got > storeMaxRetryDelay {
				t.Fatalf("attempt %d delay = %v, over the cap %v", attempt, got, storeMaxRetryDelay)
			}
			if attempt > 0 && got < prev {
				t.Fatalf("attempt %d delay = %v, shorter than the previous %v", attempt, got, prev)
			}
			prev = got
		}
		if first := storeRetryDelay(nil, 0, 0); first != 500*time.Millisecond {
			t.Errorf("first delay = %v, want 500ms", first)
		}
	})

	t.Run("jitter shortens the delay by at most a quarter", func(t *testing.T) {
		t.Parallel()

		base := storeRetryDelay(nil, 2, 0)
		jittered := storeRetryDelay(nil, 2, 1)
		if jittered >= base {
			t.Errorf("jittered delay %v is not shorter than the base %v", jittered, base)
		}
		if jittered < base-base/4 {
			t.Errorf("jittered delay %v takes more than a quarter off the base %v", jittered, base)
		}
	})
}
