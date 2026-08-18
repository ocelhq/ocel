package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
	return newTestClientFor(t, &Server{}, token)
}

func newTestClientFor(t *testing.T, deployments *Server, token string) deploymentsv1connect.DeploymentServiceClient {
	t.Helper()
	srv := httptest.NewServer(newMux(deployments, testToken))
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

func TestDeploy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		token    string
		manifest *deploymentsv1.Manifest
		want     connect.Code
	}{
		{
			name:     "a caller with no session token is unauthenticated",
			manifest: wellFormedManifest(),
			want:     connect.CodeUnauthenticated,
		},
		{
			name:     "a caller holding another session's token is unauthenticated",
			token:    "wrong-token",
			manifest: wellFormedManifest(),
			want:     connect.CodeUnauthenticated,
		},
		{
			name:     "a malformed manifest fails before any streaming",
			token:    testToken,
			manifest: &deploymentsv1.Manifest{SchemaVersion: "", Slug: "proj-123"},
			want:     connect.CodeInvalidArgument,
		},
		{
			name:  "a request carrying no manifest at all is refused",
			token: testToken,
			want:  connect.CodeInvalidArgument,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := newTestClient(t, tc.token)

			stream, err := client.Deploy(context.Background(), &deploymentsv1.DeployRequest{Manifest: tc.manifest})
			if err != nil {
				t.Fatalf("Deploy() error = %v, want nil (error surfaces on Receive)", err)
			}
			_, err = drainStream(stream)

			var connectErr *connect.Error
			if !errors.As(err, &connectErr) || connectErr.Code() != tc.want {
				t.Fatalf("Deploy() err = %v, want %v", err, tc.want)
			}
		})
	}
}

const unfrontedKind = edge.Kind("fastly")

func TestUnsupportedEdgeKind(t *testing.T) {
	t.Parallel()

	calls := []struct {
		name string
		call func(deploymentsv1connect.DeploymentServiceClient) error
	}{
		{"Deploy", func(client deploymentsv1connect.DeploymentServiceClient) error {
			stream, err := client.Deploy(context.Background(), &deploymentsv1.DeployRequest{
				Manifest: wellFormedManifest(),
				EdgeKind: string(unfrontedKind),
				Dns:      &deploymentsv1.Dns{Kind: "cloudflare", Zone: "acme.com"},
			})
			if err != nil {
				return err
			}
			_, err = drainStream(stream)
			return err
		}},
		{"Bootstrap", func(client deploymentsv1connect.DeploymentServiceClient) error {
			stream, err := client.Bootstrap(context.Background(), &deploymentsv1.BootstrapRequest{
				EdgeKind: string(unfrontedKind),
				Dns:      &deploymentsv1.Dns{Kind: "cloudflare", Zone: "acme.com"},
			})
			if err != nil {
				return err
			}
			_, err = drainStream(stream)
			return err
		}},
		{"PlanTeardown", func(client deploymentsv1connect.DeploymentServiceClient) error {
			_, err := client.PlanTeardown(context.Background(), &deploymentsv1.PlanTeardownRequest{
				EdgeKind: string(unfrontedKind),
			})
			return err
		}},
		{"Teardown", func(client deploymentsv1connect.DeploymentServiceClient) error {
			stream, err := client.Teardown(context.Background(), &deploymentsv1.TeardownRequest{
				EdgeKind: string(unfrontedKind),
			})
			if err != nil {
				return err
			}
			_, err = drainStream(stream)
			return err
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name+" refuses an edge this origin cannot front, naming the ones it can", func(t *testing.T) {
			t.Parallel()

			err := tc.call(newTestClient(t, testToken))

			var connectErr *connect.Error
			if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
				t.Fatalf("%s() err = %v, want %v", tc.name, err, connect.CodeInvalidArgument)
			}
			for _, want := range []string{string(unfrontedKind), string(edge.KindCloudflare)} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to name %q", err, want)
				}
			}
		})
	}
}
