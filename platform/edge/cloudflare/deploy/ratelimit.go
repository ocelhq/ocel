package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
)

const (
	rateLimitBudget      = 6 * time.Minute
	rateLimitMinWait     = time.Second
	rateLimitBackoffBase = 2 * time.Second
	rateLimitMaxBackoff  = time.Minute
	rateLimitMaxJitter   = 15 * time.Second
	apiMaxAttempts       = 6
)

type apiRetryPolicy struct {
	budget time.Duration
	wait   func(ctx context.Context, delay time.Duration) error
	jitter func() float64
	now    func() time.Time
}

var apiRetries = apiRetryPolicy{budget: rateLimitBudget, wait: waitBeforeRetry, jitter: retryJitter, now: time.Now}

func newAPIClient(opts ...option.RequestOption) *cf.Client {
	return apiRetries.newClient(opts...)
}

func (p apiRetryPolicy) newClient(opts ...option.RequestOption) *cf.Client {
	return cf.NewClient(append([]option.RequestOption{option.WithMaxRetries(0), option.WithMiddleware(p.send)}, opts...)...)
}

func (p apiRetryPolicy) send(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	started := p.now()
	for limits, failures := 0, 0; ; {
		res, err := next(req)
		if req.Context().Err() != nil || !apiRetryable(res, err) {
			return res, err
		}
		var delay time.Duration
		if res != nil && res.StatusCode == http.StatusTooManyRequests {
			delay = rateLimitDelay(res.Header, limits, p.jitter(), p.now())
			if p.now().Sub(started)+delay > p.budget {
				return nil, newRateLimitedError(req, res, p.now().Sub(started), delay)
			}
			limits++
		} else {
			failures++
			if failures >= apiMaxAttempts {
				return res, err
			}
			delay = storeRetryDelay(res, failures-1, p.jitter())
		}
		retry, rewound := rewindRequest(req)
		if !rewound {
			return res, err
		}
		if res != nil {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
		}
		if waitErr := p.wait(req.Context(), delay); waitErr != nil {
			return nil, waitErr
		}
		req = retry
	}
}

func apiRetryable(res *http.Response, err error) bool {
	if res == nil {
		return err != nil
	}
	switch res.StatusCode {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests:
		return true
	}
	return res.StatusCode >= http.StatusInternalServerError
}

func rewindRequest(req *http.Request) (*http.Request, bool) {
	if req.Body == nil || req.Body == http.NoBody {
		return req, true
	}
	if req.GetBody == nil {
		return nil, false
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	retry := req.Clone(req.Context())
	retry.Body = body
	return retry, true
}

func rateLimitDelay(header http.Header, attempt int, jitter float64, now time.Time) time.Duration {
	delay, asked := parseRetryAfter(header, now)
	if !asked {
		delay, asked = parseRateLimitReset(header)
	}
	if !asked {
		delay = min(time.Duration(float64(rateLimitBackoffBase)*math.Pow(2, float64(attempt))), rateLimitMaxBackoff)
	}
	delay = max(delay, rateLimitMinWait)
	return delay + time.Duration(jitter*float64(min(delay/4, rateLimitMaxJitter)))
}

func parseRateLimitReset(header http.Header) (time.Duration, bool) {
	var longest, longestExhausted time.Duration
	found, foundExhausted := false, false
	for _, item := range strings.Split(header.Get("Ratelimit"), ",") {
		reset, remaining := -1.0, -1.0
		for _, param := range strings.Split(item, ";")[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if !ok {
				continue
			}
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil || parsed < 0 {
				continue
			}
			switch key {
			case "t":
				reset = parsed
			case "r":
				remaining = parsed
			}
		}
		if reset < 0 {
			continue
		}
		wait := time.Duration(reset * float64(time.Second))
		found, longest = true, max(longest, wait)
		if remaining == 0 {
			foundExhausted, longestExhausted = true, max(longestExhausted, wait)
		}
	}
	if foundExhausted {
		return longestExhausted, true
	}
	return longest, found
}

func isRateLimited(err error) bool {
	var limited *rateLimitedError
	var answered *cf.Error
	return errors.As(err, &limited) || (errors.As(err, &answered) && answered.StatusCode == http.StatusTooManyRequests)
}

type rateLimitedError struct {
	method  string
	path    string
	waited  time.Duration
	asked   time.Duration
	message string
}

func newRateLimitedError(req *http.Request, res *http.Response, waited, asked time.Duration) *rateLimitedError {
	limited := &rateLimitedError{method: req.Method, path: req.URL.Path, waited: waited.Round(time.Second), asked: asked.Round(time.Second)}
	var body struct {
		Errors []struct {
			Code    int64  `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	_ = res.Body.Close()
	if json.Unmarshal(raw, &body) == nil {
		var messages []string
		for _, e := range body.Errors {
			messages = append(messages, fmt.Sprintf("%d: %s", e.Code, e.Message))
		}
		limited.message = strings.Join(messages, "; ")
	}
	return limited
}

func (e *rateLimitedError) Error() string {
	said := ""
	if e.message != "" {
		said = " (" + e.message + ")"
	}
	return fmt.Sprintf(
		"Cloudflare is rate limiting this account's API requests: it refused %s %s%s after ocel had waited %s, "+
			"and asked for another %s, past the %s ocel waits. "+
			"Cloudflare allows 1,200 API requests per 5 minutes for each user, across every token and the dashboard. "+
			"Wait a few minutes, or run fewer ocel commands against this Cloudflare account at once",
		e.method, e.path, said, e.waited, e.asked, rateLimitBudget)
}
