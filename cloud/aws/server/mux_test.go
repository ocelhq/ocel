package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
)

const testToken = "test-session-token"

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

func newTestClient(t *testing.T, token string) deploymentsv1connect.DeploymentServiceClient {
	t.Helper()
	srv := httptest.NewServer(NewMux(testToken))
	t.Cleanup(srv.Close)

	var opts []connect.ClientOption
	if token != "" {
		opts = append(opts, connect.WithInterceptors(authHeaderInterceptor{token: token}))
	}
	return deploymentsv1connect.NewDeploymentServiceClient(srv.Client(), srv.URL, opts...)
}

func drainStream(stream *connect.ServerStreamForClient[deploymentsv1.DeployEvent]) ([]*deploymentsv1.DeployEvent, error) {
	var events []*deploymentsv1.DeployEvent
	for stream.Receive() {
		events = append(events, stream.Msg())
	}
	return events, stream.Err()
}

func TestDeploy_RejectsMissingToken(t *testing.T) {
	client := newTestClient(t, "")

	stream, err := client.Deploy(context.Background(), &deploymentsv1.DeployRequest{Manifest: wellFormedManifest()})
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

	stream, err := client.Deploy(context.Background(), &deploymentsv1.DeployRequest{Manifest: wellFormedManifest()})
	if err != nil {
		t.Fatalf("Deploy() error = %v, want nil (error surfaces on Receive)", err)
	}
	_, err = drainStream(stream)

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
		t.Fatalf("Deploy() with wrong token err = %v, want CodeUnauthenticated", err)
	}
}

func TestDeploy_MalformedManifestFailsBeforeStreaming(t *testing.T) {
	client := newTestClient(t, testToken)

	badManifest := &deploymentsv1.Manifest{SchemaVersion: "", Slug: "proj-123"}
	stream, err := client.Deploy(context.Background(), &deploymentsv1.DeployRequest{Manifest: badManifest})
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

	stream, err := client.Deploy(context.Background(), &deploymentsv1.DeployRequest{})
	if err != nil {
		t.Fatalf("Deploy() error = %v, want nil (error surfaces on Receive)", err)
	}
	_, err = drainStream(stream)

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() with no manifest err = %v, want CodeInvalidArgument", err)
	}
}
