package bindingproxy_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/constants"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	"github.com/ocelhq/ocel/pkg/runtimekit/bindingproxy"
)

type silentBuckets struct {
	bucketv1connect.UnimplementedBucketServiceHandler
	seen int
}

func (s *silentBuckets) PresignUpload(context.Context, *bucketv1.PresignUploadRequest) (*bucketv1.PresignUploadResponse, error) {
	s.seen++
	return &bucketv1.PresignUploadResponse{SessionId: "sess_1"}, nil
}

func envValue(t *testing.T, env []string, key string) string {
	t.Helper()
	for _, entry := range env {
		if name, value, ok := strings.Cut(entry, "="); ok && name == key {
			return value
		}
	}
	t.Fatalf("env %q has no %s", env, key)
	return ""
}

type bearer string

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", channel.FormatAuthHeader(string(b)))
	return http.DefaultTransport.RoundTrip(req)
}

func TestServe(t *testing.T) {
	t.Parallel()

	svc := &silentBuckets{}
	served, err := bindingproxy.Serve(svc)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { served.Close() })

	addr := envValue(t, served.Env, constants.RuntimeAddressEnvName)
	if !strings.HasPrefix(addr, "http://127.0.0.1:") {
		t.Fatalf("%s = %q, want a loopback address nothing off the host can reach", constants.RuntimeAddressEnvName, addr)
	}
	token := envValue(t, served.Env, channel.SessionTokenEnvVar)
	if len(token) < 32 {
		t.Fatalf("%s = %q, want a token long enough not to be guessed", channel.SessionTokenEnvVar, token)
	}

	_, err = bucketv1connect.NewBucketServiceClient(http.DefaultClient, addr).
		PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{
			Bucket: "uploads",
			Files:  []*bucketv1.PresignFile{{Key: "a.png"}},
		})
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
		t.Fatalf("PresignUpload without the token err = %v, want CodeUnauthenticated", err)
	}
	if svc.seen != 0 {
		t.Fatalf("an unauthenticated call reached the service %d times", svc.seen)
	}

	if _, err := bucketv1connect.NewBucketServiceClient(&http.Client{Transport: bearer(token)}, addr).
		PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{
			Bucket: "uploads",
			Files:  []*bucketv1.PresignFile{{Key: "a.png"}},
		}); err != nil {
		t.Fatalf("PresignUpload with the minted token = %v, want it answered", err)
	}
	if svc.seen != 1 {
		t.Fatalf("the service saw %d calls, want the one the token authorized", svc.seen)
	}
}

func TestServeMintsAFreshTokenEachTime(t *testing.T) {
	t.Parallel()

	first, err := bindingproxy.Serve(&silentBuckets{})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { first.Close() })
	second, err := bindingproxy.Serve(&silentBuckets{})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { second.Close() })

	if envValue(t, first.Env, channel.SessionTokenEnvVar) == envValue(t, second.Env, channel.SessionTokenEnvVar) {
		t.Fatal("two proxies were handed the same token, so one deployment's credential opens the other")
	}
}
