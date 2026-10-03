package topics

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type timedOut struct{}

func (timedOut) Error() string   { return "timed out" }
func (timedOut) Timeout() bool   { return true }
func (timedOut) Temporary() bool { return true }

func TestOnlyTheFailuresASecondCallCanCureAreTriedAgain(t *testing.T) {
	for _, tried := range []struct {
		name  string
		err   error
		again bool
	}{
		{name: "busy", err: status.Error(codes.Unavailable, "busy"), again: true},
		{name: "too many requests", err: &googleapi.Error{Code: 429}, again: true},
		{name: "refused", err: status.Error(codes.PermissionDenied, "no")},
		{name: "forbidden", err: &googleapi.Error{Code: 403}},
		{name: "nothing at all", err: nil},
		{name: "connection reset", err: &url.Error{Op: "Post", Err: &net.OpError{Err: syscall.ECONNRESET}}, again: true},
		{name: "connection dropped", err: &url.Error{Op: "Post", Err: io.EOF}, again: true},
		{name: "timed out", err: &url.Error{Op: "Post", Err: &net.OpError{Err: timedOut{}}}, again: true},
		{name: "endpoint speaks no TLS", err: &url.Error{Op: "Post", Err: tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}}},
		{name: "untrusted certificate", err: &url.Error{Op: "Post", Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}}},
		{name: "no such host", err: &url.Error{Op: "Post", Err: &net.DNSError{Err: "no such host", Name: "pubsub.example", IsNotFound: true}}},
		{name: "malformed credentials", err: errors.New("could not parse the credentials")},
	} {
		t.Run(tried.name, func(t *testing.T) {
			if again := isRetryable(tried.err); again != tried.again {
				t.Errorf("isRetryable(%v) = %v, want %v", tried.err, again, tried.again)
			}
		})
	}
}
