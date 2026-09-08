package gcp

import (
	"context"
	"errors"
	"net"
	"net/url"
	"syscall"
	"testing"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func answered(code int) error { return &googleapi.Error{Code: code} }

func TestOnlyTheAnswersThatMeanTryAgainAreTriedAgain(t *testing.T) {
	for _, tried := range []struct {
		name  string
		err   error
		again bool
	}{
		{name: "too many requests", err: answered(429), again: true},
		{name: "bad gateway", err: answered(502), again: true},
		{name: "unavailable", err: answered(503), again: true},
		{name: "gateway timeout", err: answered(504), again: true},
		{name: "not implemented", err: answered(501)},
		{name: "internal", err: answered(500)},
		{name: "forbidden", err: answered(403)},
		{name: "malformed credentials", err: errors.New("could not parse the credentials")},
		{name: "nothing at all", err: nil},
		{name: "connection reset", err: &url.Error{Op: "Get", Err: &net.OpError{Err: syscall.ECONNRESET}}, again: true},
		{name: "timed out", err: &url.Error{Op: "Get", Err: &net.OpError{Err: timedOut{}}}, again: true},
	} {
		t.Run(tried.name, func(t *testing.T) {
			if again := retryable(answeredCode(tried.err), tried.err); again != tried.again {
				t.Errorf("retryable(%v) = %v, want %v", tried.err, again, tried.again)
			}
		})
	}
}

type timedOut struct{}

func (timedOut) Error() string   { return "timed out" }
func (timedOut) Timeout() bool   { return true }
func (timedOut) Temporary() bool { return true }

func TestACallThatKeepsFailingWithAnUnretryableAnswerIsMadeOnce(t *testing.T) {
	calls := 0
	_, err := attempted(context.Background(), func(...googleapi.CallOption) (int, error) {
		calls++
		return 0, answered(501)
	})
	if err == nil || calls != 1 {
		t.Errorf("attempted() made %d calls and returned %v, want one call: 501 says the API will never do this", calls, err)
	}
}

func TestAGrpcCallIsTriedAgainWhileTheServiceSaysItIsBusy(t *testing.T) {
	calls := 0
	got, err := dialled(context.Background(), func() (int, error) {
		calls++
		if calls < 3 {
			return 0, status.Error(codes.ResourceExhausted, "quota")
		}
		return 7, nil
	})
	if err != nil || calls != 3 || got != 7 {
		t.Fatalf("dialled() made %d calls and returned %d, %v, want the busy answers tried again", calls, got, err)
	}
}

func TestACallOutsideGrpcIsTriedAgainWhenTheAnswerSaysTryAgain(t *testing.T) {
	calls := 0
	err := done(context.Background(), func() error {
		calls++
		if calls < 2 {
			return answered(503)
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("done() made %d calls and returned %v, want the unavailable answer tried again", calls, err)
	}
}

func TestAGrpcCallThatIsRefusedOutrightIsMadeOnce(t *testing.T) {
	calls := 0
	_, err := dialled(context.Background(), func() (int, error) {
		calls++
		return 0, status.Error(codes.PermissionDenied, "no")
	})
	if err == nil || calls != 1 {
		t.Errorf("dialled() made %d calls and returned %v, want one call", calls, err)
	}
}
