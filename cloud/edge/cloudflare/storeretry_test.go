package cloudflare

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestStoreRetryable_TransportFailureRetries(t *testing.T) {
	if !storeRetryable(nil, errors.New("connection refused")) {
		t.Error("a request that never reached the store must retry")
	}
}

func TestStoreRetryable_ByStatus(t *testing.T) {
	cases := map[int]bool{
		http.StatusOK:                  false,
		http.StatusUnauthorized:        false,
		http.StatusConflict:            false,
		http.StatusNotFound:            false,
		http.StatusTooManyRequests:     true,
		http.StatusInternalServerError: true,
		http.StatusBadGateway:          true,
		http.StatusServiceUnavailable:  true,
		http.StatusGatewayTimeout:      true,
	}
	for status, want := range cases {
		res := &http.Response{StatusCode: status, Header: http.Header{}}
		if got := storeRetryable(res, nil); got != want {
			t.Errorf("storeRetryable(%d) = %v, want %v", status, got, want)
		}
	}
}

func TestParseStoreRetryAfter_PrefersMilliseconds(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After-Ms", "250")
	h.Set("Retry-After", "30")

	got, ok := parseStoreRetryAfter(h, time.Now())
	if !ok || got != 250*time.Millisecond {
		t.Errorf("parseStoreRetryAfter = %v, %v; want 250ms, true", got, ok)
	}
}

func TestParseStoreRetryAfter_SecondsAndHTTPDate(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	h := http.Header{}
	h.Set("Retry-After", "2")
	if got, ok := parseStoreRetryAfter(h, now); !ok || got != 2*time.Second {
		t.Errorf("seconds form = %v, %v; want 2s, true", got, ok)
	}

	h = http.Header{}
	h.Set("Retry-After", now.Add(3*time.Second).Format(time.RFC1123))
	if got, ok := parseStoreRetryAfter(h, now); !ok || got != 3*time.Second {
		t.Errorf("HTTP-date form = %v, %v; want 3s, true", got, ok)
	}
}

func TestParseStoreRetryAfter_UnusableValues(t *testing.T) {
	for _, value := range []string{"", "soon", "-1"} {
		h := http.Header{}
		if value != "" {
			h.Set("Retry-After", value)
		}
		if _, ok := parseStoreRetryAfter(h, time.Now()); ok {
			t.Errorf("Retry-After %q must not be honored", value)
		}
	}
}

// A store that asks for longer than the deploy can reasonably wait is ignored in
// favour of the capped backoff, rather than stalling the deploy on its word.
func TestStoreRetryDelay_IgnoresAnUnreasonableRetryAfter(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "600")
	res := &http.Response{StatusCode: http.StatusTooManyRequests, Header: h}

	if got := storeRetryDelay(res, 0, 0); got > storeMaxRetryDelay {
		t.Errorf("delay = %v, want at most the capped backoff %v", got, storeMaxRetryDelay)
	}
}

func TestStoreRetryDelay_HonorsAReasonableRetryAfter(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After-Ms", "1500")
	res := &http.Response{StatusCode: http.StatusServiceUnavailable, Header: h}

	if got := storeRetryDelay(res, 3, 0.9); got != 1500*time.Millisecond {
		t.Errorf("delay = %v, want the requested 1.5s regardless of attempt or jitter", got)
	}
}

func TestStoreRetryDelay_BacksOffExponentiallyUnderACap(t *testing.T) {
	var prev time.Duration
	for attempt := 0; attempt < 8; attempt++ {
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
}

// Jitter spreads concurrent deploys that all backed off from the same throttle,
// which is the whole point of the delay being random rather than fixed.
func TestStoreRetryDelay_JitterShortensWithinAQuarter(t *testing.T) {
	base := storeRetryDelay(nil, 2, 0)
	jittered := storeRetryDelay(nil, 2, 1)
	if jittered >= base {
		t.Errorf("jittered delay %v is not shorter than the base %v", jittered, base)
	}
	if jittered < base-base/4 {
		t.Errorf("jittered delay %v takes more than a quarter off the base %v", jittered, base)
	}
}
