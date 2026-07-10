package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

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
		req.Header().Set("Authorization", providerv1.FormatAuthHeader(a.token))
		return next(ctx, req)
	}
}

func (a authHeaderInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", providerv1.FormatAuthHeader(a.token))
		return conn
	}
}

func (a authHeaderInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// newTestClient starts an httptest server over newMux(testToken) and returns
// a client. When token is non-empty, every call presents it as the
// Authorization header; an empty token presents no header at all.
func newTestClient(t *testing.T, token string) providerv1connect.ProviderServiceClient {
	t.Helper()
	srv := httptest.NewServer(newMux(testToken))
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

func TestDeploy_StreamsProgressThenSuccess(t *testing.T) {
	client := newTestClient(t, testToken)

	stream, err := client.Deploy(context.Background(), &providerv1.DeployRequest{Manifest: wellFormedManifest()})
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}
	events, err := drainStream(stream)
	if err != nil {
		t.Fatalf("Deploy() stream error = %v", err)
	}

	if len(events) < 2 {
		t.Fatalf("got %d events, want at least 2 (progress + terminal result)", len(events))
	}
	for _, e := range events[:len(events)-1] {
		if e.GetProgress() == nil && e.GetLog() == nil {
			t.Fatalf("non-terminal event %+v is neither progress nor log", e)
		}
	}
	last := events[len(events)-1]
	result := last.GetResult()
	if result == nil {
		t.Fatalf("last event %+v is not a ResultEvent", last)
	}
	if !result.GetSuccess() {
		t.Fatalf("ResultEvent.Success = false, want true; error = %q", result.GetError())
	}
}

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
