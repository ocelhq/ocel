package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/v1/providerv1connect"
)

const testToken = "test-session-token"

// authHeaderInterceptor is a client-side interceptor standing in for what
// the CLI does: attach the session token to every call's Authorization
// header. The generated ProviderServiceClient.Deploy signature takes the
// bare request message (no per-call header option), so tests need this to
// exercise the auth path at all.
type authHeaderInterceptor struct{ token string }

func (a authHeaderInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", channel.FormatAuthHeader(a.token))
		return next(ctx, req)
	}
}

func (a authHeaderInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", channel.FormatAuthHeader(a.token))
		return conn
	}
}

func (a authHeaderInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// newTestClient starts an httptest server over NewMux(testToken) and returns
// a client. When token is non-empty, every call presents it as the
// Authorization header; an empty token presents no header at all.
func newTestClient(t *testing.T, token string) providerv1connect.ProviderServiceClient {
	t.Helper()
	srv := httptest.NewServer(NewMux(testToken))
	t.Cleanup(srv.Close)

	var opts []connect.ClientOption
	if token != "" {
		opts = append(opts, connect.WithInterceptors(authHeaderInterceptor{token: token}))
	}
	return providerv1connect.NewProviderServiceClient(srv.Client(), srv.URL, opts...)
}

func drainStream(stream *connect.ServerStreamForClient[providerv1.DeployEvent]) ([]*providerv1.DeployEvent, error) {
	var events []*providerv1.DeployEvent
	for stream.Receive() {
		events = append(events, stream.Msg())
	}
	return events, stream.Err()
}

func TestDeploy_RejectsMissingToken(t *testing.T) {
	client := newTestClient(t, "")

	stream, err := client.Deploy(context.Background(), &providerv1.DeployRequest{Manifest: wellFormedManifest()})
	if err != nil {
		t.Fatalf("Deploy() error = %v, want nil (error surfaces on Receive)", err)
	}
	_, err = drainStream(stream)

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
		t.Fatalf("Deploy() with no token err = %v, want CodeUnauthenticated", err)
	}
}

func TestDeploy_RejectsWrongToken(t *testing.T) {
	client := newTestClient(t, "wrong-token")

	stream, err := client.Deploy(context.Background(), &providerv1.DeployRequest{Manifest: wellFormedManifest()})
	if err != nil {
		t.Fatalf("Deploy() error = %v, want nil (error surfaces on Receive)", err)
	}
	_, err = drainStream(stream)

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
		t.Fatalf("Deploy() with wrong token err = %v, want CodeUnauthenticated", err)
	}
}

// The happy-path "streams progress then success" assertion moved to the
// opt-in, build-tagged real e2e (deploy_e2e_test in the CLI, //go:build
// awslive): a successful Deploy now provisions real Aurora/S3/CFN, which
// must never run in CI. The pre-provision behaviour a unit test can pin —
// token auth and manifest validation, both of which reject before any AWS
// call — is covered by the tests around this one.

func TestDeploy_MalformedManifestFailsBeforeStreaming(t *testing.T) {
	client := newTestClient(t, testToken)

	badManifest := &providerv1.Manifest{SchemaVersion: "", ProjectId: "proj_123"}
	stream, err := client.Deploy(context.Background(), &providerv1.DeployRequest{Manifest: badManifest})
	if err != nil {
		t.Fatalf("Deploy() error = %v, want nil (error surfaces on Receive)", err)
	}
	_, err = drainStream(stream)

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() with malformed manifest err = %v, want CodeInvalidArgument", err)
	}
}

func TestDeploy_MissingManifestRejected(t *testing.T) {
	client := newTestClient(t, testToken)

	stream, err := client.Deploy(context.Background(), &providerv1.DeployRequest{})
	if err != nil {
		t.Fatalf("Deploy() error = %v, want nil (error surfaces on Receive)", err)
	}
	_, err = drainStream(stream)

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() with no manifest err = %v, want CodeInvalidArgument", err)
	}
}
