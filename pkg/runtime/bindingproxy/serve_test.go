package bindingproxy_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
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
	req.Header.Set("Authorization", localrpc.FormatAuthHeader(string(b)))
	return http.DefaultTransport.RoundTrip(req)
}

func TestServe(t *testing.T) {
	t.Parallel()

	svc := &silentBuckets{}
	served, err := bindingproxy.Serve(bindingproxy.Services{Buckets: svc})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { served.Close() })

	addr := envValue(t, served.Env, processenv.RuntimeAddressEnvVar)
	if !strings.HasPrefix(addr, "http://127.0.0.1:") {
		t.Fatalf("%s = %q, want a loopback address nothing off the host can reach", processenv.RuntimeAddressEnvVar, addr)
	}
	token := envValue(t, served.Env, localrpc.SessionTokenEnvVar)
	if len(token) < 32 {
		t.Fatalf("%s = %q, want a token long enough not to be guessed", localrpc.SessionTokenEnvVar, token)
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

	first, err := bindingproxy.Serve(bindingproxy.Services{Buckets: &silentBuckets{}})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { first.Close() })
	second, err := bindingproxy.Serve(bindingproxy.Services{Buckets: &silentBuckets{}})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { second.Close() })

	if envValue(t, first.Env, localrpc.SessionTokenEnvVar) == envValue(t, second.Env, localrpc.SessionTokenEnvVar) {
		t.Fatal("two proxies were handed the same token, so one deployment's credential opens the other")
	}
}

func TestServeNamesItsAddressAndTokenForACallerThatDeliversThemItself(t *testing.T) {
	t.Parallel()

	served, err := bindingproxy.Serve(bindingproxy.Services{Buckets: &silentBuckets{}})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { served.Close() })

	if want := envValue(t, served.Env, processenv.RuntimeAddressEnvVar); served.Address != want {
		t.Errorf("Address = %q, want %q: the address the env delivers", served.Address, want)
	}
	if want := envValue(t, served.Env, localrpc.SessionTokenEnvVar); served.Token != want {
		t.Errorf("Token = %q, want the token the env delivers", served.Token)
	}
}

func TestServedGrantsReportNothingWhenClosedOnPurpose(t *testing.T) {
	t.Parallel()

	var reported []error
	served, err := bindingproxy.ServeGrants([]bindingproxy.Grant{{Grantee: "web", Services: bindingproxy.Services{Buckets: &silentBuckets{}}}}, func(err error) { reported = append(reported, err) })
	if err != nil {
		t.Fatalf("ServeGrants: %v", err)
	}

	if err := served.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if len(reported) != 0 {
		t.Errorf("a deliberate Close reported %v, want nothing: it is no failure", reported)
	}
}

func presign(token, address string) error {
	_, err := bucketv1connect.NewBucketServiceClient(&http.Client{Transport: bearer(token)}, address).
		PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{Bucket: "uploads", Files: []*bucketv1.PresignFile{{Key: "a.png"}}})
	return err
}

func TestEachGrantIsMintedItsOwnTokenThatReachesOnlyItsOwnServices(t *testing.T) {
	t.Parallel()

	web, docs := &silentBuckets{}, &silentBuckets{}
	served, err := bindingproxy.ServeGrants([]bindingproxy.Grant{
		{Grantee: "web", Services: bindingproxy.Services{Buckets: web}},
		{Grantee: "docs", Services: bindingproxy.Services{Buckets: docs}},
	}, func(error) {})
	if err != nil {
		t.Fatalf("ServeGrants: %v", err)
	}
	t.Cleanup(func() { served.Close() })

	tokens := map[string]string{}
	for _, session := range served.Sessions {
		tokens[session.Grantee] = session.Token
	}
	if len(tokens) != 2 || tokens["web"] == "" || tokens["web"] == tokens["docs"] {
		t.Fatalf("Sessions = %+v, want a distinct token for each grantee", served.Sessions)
	}

	if err := presign(tokens["web"], served.Address); err != nil {
		t.Fatalf("PresignUpload with web's token = %v", err)
	}
	if web.seen != 1 || docs.seen != 0 {
		t.Errorf("web's call reached web %d and docs %d times, want 1 and 0", web.seen, docs.seen)
	}
	if err := presign(tokens["docs"], served.Address); err != nil {
		t.Fatalf("PresignUpload with docs' token = %v", err)
	}
	if web.seen != 1 || docs.seen != 1 {
		t.Errorf("docs' call left web at %d and docs at %d, want 1 and 1", web.seen, docs.seen)
	}
}

func TestAGrantWithoutABucketServiceIsRefusedTheBucketService(t *testing.T) {
	t.Parallel()

	served, err := bindingproxy.ServeGrants([]bindingproxy.Grant{
		{Grantee: "web", Services: bindingproxy.Services{Buckets: &silentBuckets{}}},
		{Grantee: "docs", Services: bindingproxy.Services{}},
	}, func(error) {})
	if err != nil {
		t.Fatalf("ServeGrants: %v", err)
	}
	t.Cleanup(func() { served.Close() })

	var docs string
	for _, session := range served.Sessions {
		if session.Grantee == "docs" {
			docs = session.Token
		}
	}
	if err := presign(docs, served.Address); connect.CodeOf(err) != connect.CodeUnimplemented && connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("PresignUpload with a token granted no bucket service = %v, want it refused", err)
	}
}

func TestATokenNoGrantWasMintedIsRefusedWhicheverGrantsAreServed(t *testing.T) {
	t.Parallel()

	web := &silentBuckets{}
	served, err := bindingproxy.ServeGrants([]bindingproxy.Grant{{Grantee: "web", Services: bindingproxy.Services{Buckets: web}}}, func(error) {})
	if err != nil {
		t.Fatalf("ServeGrants: %v", err)
	}
	t.Cleanup(func() { served.Close() })

	if err := presign("not-a-minted-token", served.Address); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("PresignUpload with an unminted token = %v, want Unauthenticated", err)
	}
	if web.seen != 0 {
		t.Errorf("an unminted token reached the service %d times", web.seen)
	}
}
