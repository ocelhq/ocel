package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cloud/aws/vars"
	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
	"github.com/ocelhq/ocel/pkg/proto/env/v1/envv1connect"
)

func newTestVarsClient(t *testing.T, token string) envv1connect.EnvVarsServiceClient {
	t.Helper()
	srv := httptest.NewServer(NewMux(testToken))
	t.Cleanup(srv.Close)

	var opts []connect.ClientOption
	if token != "" {
		opts = append(opts, connect.WithInterceptors(authHeaderInterceptor{token: token}))
	}
	return envv1connect.NewEnvVarsServiceClient(srv.Client(), srv.URL, opts...)
}

// The vars service must be served on the provider's own channel, behind the
// same session-token handshake: it reaches values in the user's cloud account,
// so an unauthenticated caller must not get past the interceptor to the store.
func TestListValues_RejectsMissingToken(t *testing.T) {
	_, err := newTestVarsClient(t, "").ListValues(context.Background(), &envv1.ListValuesRequest{Slug: "shop"})

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
		t.Fatalf("ListValues with no token err = %v, want CodeUnauthenticated", err)
	}
}

func TestListValues_RejectsWrongToken(t *testing.T) {
	_, err := newTestVarsClient(t, "wrong-token").ListValues(context.Background(), &envv1.ListValuesRequest{Slug: "shop"})

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
		t.Fatalf("ListValues with wrong token err = %v, want CodeUnauthenticated", err)
	}
}

// A lost race has to be distinguishable from a broken request by code, so the
// CLI can tell the operator to re-read and retry without matching on text.
func TestVarsError_ClassifiesStoreFailures(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want connect.Code
	}{
		{vars.ErrStaleVersion, connect.CodeFailedPrecondition},
		{vars.ErrNotFound, connect.CodeNotFound},
	} {
		var connectErr *connect.Error
		if !errors.As(varsError(tc.err), &connectErr) || connectErr.Code() != tc.want {
			t.Errorf("varsError(%v) = %v, want %v", tc.err, varsError(tc.err), tc.want)
		}
	}

	other := errors.New("the table is on fire")
	var connectErr *connect.Error
	if errors.As(varsError(other), &connectErr) {
		t.Errorf("varsError(%v) = %v, want it left unclassified", other, varsError(other))
	}
}

func TestCoordinateRoundTripsThroughTheWire(t *testing.T) {
	want := vars.Coordinate{Slug: "shop", Folder: "/checkout", Key: "STRIPE_API_KEY", Environment: "staging"}
	if got := toCoordinate(toCoordinateProto(want)); got != want {
		t.Errorf("coordinate round trip = %+v, want %+v", got, want)
	}
}
