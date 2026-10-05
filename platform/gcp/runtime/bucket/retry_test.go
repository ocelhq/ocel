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
)

func askedTimes(t *testing.T, status int, failure error) int {
	t.Helper()
	asked := 0
	_, _ = retried(context.Background(), func() (struct{}, int, error) {
		asked++
		return struct{}{}, status, failure
	})
	return asked
}

func TestAFailureThatWillFailAgainIsAskedOnce(t *testing.T) {
	t.Parallel()

	for name, failure := range map[string]error{
		"an answer that is not a Cloud Storage one": fmt.Errorf("decode the answer: %w", errors.New("invalid character '<'")),
		"a request that cannot be sent":             &url.Error{Op: "Get", URL: "ftp://x", Err: errors.New("unsupported protocol scheme")},
	} {
		if asked := askedTimes(t, 0, failure); asked != 1 {
			t.Errorf("%s was asked %d times, want 1: asking again fails the same way", name, asked)
		}
	}
	if asked := askedTimes(t, http.StatusForbidden, errors.New("forbidden")); asked != 1 {
		t.Errorf("a refusal was asked %d times, want 1", asked)
	}
}

func TestAFailureOfTheNetworkOrTheServiceIsAskedAgain(t *testing.T) {
	t.Parallel()

	reset := &url.Error{Op: "Post", URL: "https://storage.googleapis.com", Err: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}}
	if asked := askedTimes(t, 0, reset); asked != askAttempts {
		t.Errorf("a reset connection was asked %d times, want %d", asked, askAttempts)
	}
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusRequestTimeout} {
		if asked := askedTimes(t, status, errors.New(http.StatusText(status))); asked != askAttempts {
			t.Errorf("an answer of %d was asked %d times, want %d", status, asked, askAttempts)
		}
	}
}
