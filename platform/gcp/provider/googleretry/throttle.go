package googleretry

import (
	"errors"
	"net/http"
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

func isThrottled(code int, err error) bool {
	if code != 0 {
		return code == http.StatusTooManyRequests
	}
	return status.Code(err) == codes.ResourceExhausted
}

func (c clock) throttleWait(err error, throttles int) time.Duration {
	if delay, asked := askedDelay(err, c.now()); asked {
		delay = max(delay, throttleBackoff)
		return delay + c.random(delay/4)
	}
	limit := throttleBackoff
	for doubled := 0; doubled < throttles && limit < throttleCeiling; doubled++ {
		limit *= 2
	}
	return c.random(min(limit, throttleCeiling))
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
