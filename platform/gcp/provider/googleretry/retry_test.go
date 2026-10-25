package googleretry_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

type timedOut struct{}

func (timedOut) Error() string   { return "timed out" }
func (timedOut) Timeout() bool   { return true }
func (timedOut) Temporary() bool { return true }

func answered(code int) error { return &googleapi.Error{Code: code} }

func dialFailed(err error) error {
	return &url.Error{Op: "Get", URL: "https://storage.googleapis.com", Err: &net.OpError{Op: "dial", Net: "tcp", Err: err}}
}

func TestOnlyTheFailuresASecondCallCanCureAreTriedAgain(t *testing.T) {
	t.Parallel()

	for _, tried := range []struct {
		name  string
		err   error
		again bool
	}{
		{name: "request timeout", err: answered(http.StatusRequestTimeout), again: true},
		{name: "internal", err: answered(http.StatusInternalServerError), again: true},
		{name: "bad gateway", err: answered(http.StatusBadGateway), again: true},
		{name: "answer unavailable", err: answered(http.StatusServiceUnavailable), again: true},
		{name: "gateway timeout", err: answered(http.StatusGatewayTimeout), again: true},
		{name: "not implemented", err: answered(http.StatusNotImplemented)},
		{name: "HTTP version not supported", err: answered(http.StatusHTTPVersionNotSupported)},
		{name: "forbidden", err: answered(http.StatusForbidden)},
		{name: "conflict", err: answered(http.StatusConflict)},
		{name: "busy", err: status.Error(codes.Unavailable, "busy"), again: true},
		{name: "deadline", err: status.Error(codes.DeadlineExceeded, "slow"), again: true},
		{name: "aborted", err: status.Error(codes.Aborted, "contended"), again: true},
		{name: "refused", err: status.Error(codes.PermissionDenied, "no")},
		{name: "invalid", err: status.Error(codes.InvalidArgument, "bad")},
		{name: "connection reset", err: &url.Error{Op: "Post", Err: &net.OpError{Err: syscall.ECONNRESET}}, again: true},
		{name: "connection aborted", err: &url.Error{Op: "Post", Err: &net.OpError{Err: syscall.ECONNABORTED}}, again: true},
		{name: "broken pipe", err: &url.Error{Op: "Post", Err: &net.OpError{Err: syscall.EPIPE}}, again: true},
		{name: "timed out", err: &url.Error{Op: "Post", Err: &net.OpError{Err: timedOut{}}}, again: true},
		{name: "connection dropped", err: &url.Error{Op: "Post", Err: io.EOF}, again: true},
		{name: "answer cut short", err: &url.Error{Op: "Get", Err: io.ErrUnexpectedEOF}, again: true},
		{name: "connection closed under the request", err: &url.Error{Op: "Post", Err: &net.OpError{Op: "read", Err: net.ErrClosed}}, again: true},
		{name: "endpoint speaks no TLS", err: &url.Error{Op: "Get", Err: tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}}},
		{name: "untrusted certificate", err: &url.Error{Op: "Get", Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}}},
		{name: "no such host", err: &url.Error{Op: "Get", Err: &net.DNSError{Err: "no such host", Name: "logging.example", IsNotFound: true}}},
		{name: "no such host while dialling", err: dialFailed(&net.DNSError{Err: "no such host", Name: "storage.example", IsNotFound: true})},
		{name: "connection refused", err: dialFailed(syscall.ECONNREFUSED)},
		{name: "request that cannot be sent", err: &url.Error{Op: "Get", URL: "ftp://x", Err: errors.New("unsupported protocol scheme")}},
		{name: "malformed credentials", err: errors.New("could not parse the credentials")},
	} {
		t.Run(tried.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			_, err := googleretry.Called(context.Background(), func() (struct{}, error) {
				calls++
				return struct{}{}, tried.err
			})
			want := 1
			if tried.again {
				want = googleretry.Attempts
			}
			if calls != want || !errors.Is(err, tried.err) {
				t.Errorf("Called() made %d calls and returned %v, want %d calls and %v", calls, err, want, tried.err)
			}
		})
	}
}

func TestACallThatSucceedsIsMadeOnce(t *testing.T) {
	t.Parallel()

	calls := 0
	got, err := googleretry.Called(context.Background(), func() (int, error) {
		calls++
		return 7, nil
	})
	if err != nil || got != 7 || calls != 1 {
		t.Errorf("Called() made %d calls and returned %d, %v, want one call answering 7", calls, got, err)
	}
}

func TestACallIsTriedAgainUntilTheServiceStopsSayingItIsBusy(t *testing.T) {
	t.Parallel()

	calls := 0
	got, err := googleretry.Called(context.Background(), func() (int, error) {
		calls++
		if calls < 3 {
			return 0, status.Error(codes.ResourceExhausted, "quota")
		}
		return 7, nil
	})
	if err != nil || calls != 3 || got != 7 {
		t.Fatalf("Called() made %d calls and returned %d, %v, want the busy answers tried again", calls, got, err)
	}
}

func TestAGoogleAPICallIsTriedAgainWhenTheAnswerSaysTryAgain(t *testing.T) {
	t.Parallel()

	calls := 0
	got, err := googleretry.Attempted(context.Background(), func(...googleapi.CallOption) (string, error) {
		calls++
		if calls < 2 {
			return "", answered(http.StatusServiceUnavailable)
		}
		return "served", nil
	})
	if err != nil || calls != 2 || got != "served" {
		t.Fatalf("Attempted() made %d calls and returned %q, %v, want the unavailable answer tried again", calls, got, err)
	}
}

func TestAGoogleAPICallRefusedOutrightIsMadeOnce(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := googleretry.Attempted(context.Background(), func(...googleapi.CallOption) (int, error) {
		calls++
		return 0, answered(http.StatusNotImplemented)
	})
	if err == nil || calls != 1 {
		t.Errorf("Attempted() made %d calls and returned %v, want one call: 501 says the API will never do this", calls, err)
	}
}

func TestACallWithNothingToAnswerIsTriedAgainWhenTheAnswerSaysTryAgain(t *testing.T) {
	t.Parallel()

	calls := 0
	err := googleretry.Done(context.Background(), func() error {
		calls++
		if calls < 2 {
			return answered(http.StatusServiceUnavailable)
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("Done() made %d calls and returned %v, want the unavailable answer tried again", calls, err)
	}
}

func TestAnAnswerThatSaysTryAgainIsAskedAgainEvenWithoutAnError(t *testing.T) {
	t.Parallel()

	calls := 0
	got, code, err := googleretry.Asked(context.Background(), func() (string, int, error) {
		calls++
		if calls < 2 {
			return "", http.StatusServiceUnavailable, nil
		}
		return "principal", http.StatusOK, nil
	})
	if err != nil || code != http.StatusOK || got != "principal" || calls != 2 {
		t.Fatalf("Asked() made %d calls and returned %q, %d, %v, want the unavailable answer asked again", calls, got, code, err)
	}
}

func TestAnAnswerCutShortAfterItArrivedIsNotAskedAgain(t *testing.T) {
	t.Parallel()

	calls := 0
	_, code, err := googleretry.Asked(context.Background(), func() ([]byte, int, error) {
		calls++
		return nil, http.StatusOK, syscall.ECONNRESET
	})
	if calls != 1 || code != http.StatusOK || !errors.Is(err, syscall.ECONNRESET) {
		t.Errorf("Asked() made %d calls and returned %d, %v, want one call: the status says the request was served", calls, code, err)
	}
}

func TestARetryEndsWithTheRunsOwnErrorOnceTheRunIsCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	_, err := googleretry.Called(ctx, func() (int, error) {
		calls++
		return 0, answered(http.StatusServiceUnavailable)
	})
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Errorf("Called() made %d calls and returned %v, want one call and the run's cancellation", calls, err)
	}
}

func TestTheCodeOfAnAnswerIsReadThroughWrapping(t *testing.T) {
	t.Parallel()

	for _, read := range []struct {
		name string
		err  error
		code int
	}{
		{name: "answer", err: answered(http.StatusConflict), code: http.StatusConflict},
		{name: "wrapped answer", err: errors.Join(errors.New("create the topic"), answered(http.StatusNotFound)), code: http.StatusNotFound},
		{name: "no answer", err: syscall.ECONNRESET},
		{name: "nothing at all"},
	} {
		if code := googleretry.AnsweredCode(read.err); code != read.code {
			t.Errorf("AnsweredCode(%s) = %d, want %d", read.name, code, read.code)
		}
	}
}

func TestEveryAttemptABackoffAllowsEndsWithoutPanickingWhenTheRunIsCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for attempt := 1; attempt < 200; attempt++ {
		if googleretry.Waited(ctx, attempt) {
			t.Fatalf("Waited() at attempt %d = true, want false for a cancelled run", attempt)
		}
		if googleretry.WaitedFor(ctx, attempt, time.Minute) {
			t.Fatalf("WaitedFor() at attempt %d = true, want false for a cancelled run", attempt)
		}
	}
}

func TestAWriteRefusedAsConflictingPreconditionFailedOrAbortedIsStale(t *testing.T) {
	for name, err := range map[string]error{
		"http 409":          &googleapi.Error{Code: http.StatusConflict},
		"http 412":          &googleapi.Error{Code: http.StatusPreconditionFailed},
		"grpc aborted":      status.Error(codes.Aborted, "etag"),
		"grpc precondition": status.Error(codes.FailedPrecondition, "etag"),
	} {
		if !googleretry.IsStale(err) {
			t.Errorf("IsStale(%s) = false, want true", name)
		}
	}
	for name, err := range map[string]error{
		"http 403":       &googleapi.Error{Code: http.StatusForbidden},
		"grpc not found": status.Error(codes.NotFound, "gone"),
		"nil":            nil,
	} {
		if googleretry.IsStale(err) {
			t.Errorf("IsStale(%s) = true, want false", name)
		}
	}
}
