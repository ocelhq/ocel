package googleretry

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/googleapis/gax-go/v2/apierror"
	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	throttleBudget  = 5 * time.Minute
	throttleBackoff = time.Second
	throttleCeiling = time.Minute
)

const (
	rateLimitReason       = "rateLimitExceeded"
	rateLimitDetailReason = "RATE_LIMIT_EXCEEDED"
)

func isThrottled(code int, err error) bool {
	switch code {
	case 0:
		return status.Code(err) == codes.ResourceExhausted
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		return isRateLimited(err)
	}
	return false
}

func isRateLimited(err error) bool {
	var answered *googleapi.Error
	if errors.As(err, &answered) && slices.ContainsFunc(answered.Errors, func(item googleapi.ErrorItem) bool { return item.Reason == rateLimitReason }) {
		return true
	}
	return errorDetails(err).ErrorInfo.GetReason() == rateLimitDetailReason
}

func (c clock) throttleWait(err error, throttles int) time.Duration {
	if delay, asked := askedDelay(err, c.now()); asked {
		delay = max(delay, throttleBackoff)
		return delay + c.random(delay/4)
	}
	return c.random(doubled(throttleBackoff, throttles, throttleCeiling))
}

func askedDelay(err error, now time.Time) (time.Duration, bool) {
	var answered *googleapi.Error
	if errors.As(err, &answered) {
		if delay, asked := retryAfter(answered.Header.Get("Retry-After"), now); asked {
			return delay, true
		}
	}
	if delay := errorDetails(err).RetryInfo.GetRetryDelay(); delay != nil {
		return delay.AsDuration(), true
	}
	return 0, false
}

func retryAfter(header string, now time.Time) (time.Duration, bool) {
	if header == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(header); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}

func errorDetails(err error) apierror.ErrDetails {
	var parsed *apierror.APIError
	if !errors.As(err, &parsed) {
		parsed, _ = apierror.ParseError(err, false)
	}
	if parsed == nil {
		return apierror.ErrDetails{}
	}
	return parsed.Details()
}
