package bucket

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"testing"

	"google.golang.org/api/googleapi"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

func askedTimes(t *testing.T, failure error) int {
	t.Helper()
	calls := 0
	_, _ = asked(context.Background(), func(...googleapi.CallOption) (struct{}, error) {
		calls++
		return struct{}{}, failure
	})
	return calls
}

func dialFailed(err error) error {
	return &url.Error{Op: "Get", URL: "https://storage.googleapis.com", Err: &net.OpError{Op: "dial", Net: "tcp", Err: err}}
}

func TestAFailureThatWillFailAgainIsAskedOnce(t *testing.T) {
	t.Parallel()

	for name, failure := range map[string]error{
		"an answer that is not a Cloud Storage one": fmt.Errorf("decode the answer: %w", errors.New("invalid character '<'")),
		"a request that cannot be sent":             &url.Error{Op: "Get", URL: "ftp://x", Err: errors.New("unsupported protocol scheme")},
		"a refusal":                                 &googleapi.Error{Code: http.StatusForbidden},
		"an answer the API will never give":         &googleapi.Error{Code: http.StatusNotImplemented},
		"a host that does not exist":                dialFailed(&net.DNSError{Err: "no such host", Name: "storage.example", IsNotFound: true}),
		"a refused connection":                      dialFailed(syscall.ECONNREFUSED),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if asked := askedTimes(t, failure); asked != 1 {
				t.Errorf("%s was asked %d times, want 1: asking again fails the same way", name, asked)
			}
		})
	}
}

func TestAFailureOfTheNetworkOrTheServiceIsAskedAgain(t *testing.T) {
	t.Parallel()

	for name, failure := range map[string]error{
		"a reset connection":  &url.Error{Op: "Post", URL: "https://storage.googleapis.com", Err: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}},
		"unavailable":         &googleapi.Error{Code: http.StatusServiceUnavailable},
		"a request timed out": &googleapi.Error{Code: http.StatusRequestTimeout},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if asked := askedTimes(t, failure); asked != googleretry.Attempts {
				t.Errorf("%s was asked %d times, want %d", name, asked, googleretry.Attempts)
			}
		})
	}
}

func TestAThrottledRequestIsAskedAgainOnceTheThrottlePasses(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := asked(context.Background(), func(...googleapi.CallOption) (struct{}, error) {
		calls++
		if calls == 1 {
			return struct{}{}, &googleapi.Error{Code: http.StatusTooManyRequests}
		}
		return struct{}{}, nil
	})
	if err != nil || calls != 2 {
		t.Errorf("asked() made %d calls and returned %v, want the throttled request asked again and served", calls, err)
	}
}
