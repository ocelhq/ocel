package bindingproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/realtime/v1/realtimev1connect"
)

type recordingRealtime struct {
	realtimev1connect.UnimplementedRealtimeServiceHandler
	published []*realtimev1.PublishRequest
}

func (r *recordingRealtime) Publish(_ context.Context, req *realtimev1.PublishRequest) (*realtimev1.PublishResponse, error) {
	r.published = append(r.published, req)
	return &realtimev1.PublishResponse{}, nil
}

func serveRealtime(t *testing.T) (realtimev1connect.RealtimeServiceClient, *recordingRealtime) {
	t.Helper()
	svc := &recordingRealtime{}
	ts := httptest.NewServer(NewMux(testToken, Services{Realtime: svc}))
	t.Cleanup(ts.Close)
	return realtimev1connect.NewRealtimeServiceClient(http.DefaultClient, ts.URL, connect.WithInterceptors(bearer{token: testToken})), svc
}

func TestAPublishOnAChannelOfTheResourceReachesTheRealtimeService(t *testing.T) {
	t.Parallel()

	client, svc := serveRealtime(t)
	if _, err := client.Publish(context.Background(), &realtimev1.PublishRequest{Realtime: "app", Channel: "/app/orders/o-1", Event: `{"v":1}`}); err != nil {
		t.Fatalf("Publish() = %v", err)
	}
	if len(svc.published) != 1 || svc.published[0].GetChannel() != "/app/orders/o-1" {
		t.Errorf("the realtime service received %v, want the one publish", svc.published)
	}
}

func TestAPublishOnAnotherResourcesChannelIsRefusedBeforeItReachesTheService(t *testing.T) {
	t.Parallel()

	client, svc := serveRealtime(t)
	_, err := client.Publish(context.Background(), &realtimev1.PublishRequest{Realtime: "app", Channel: "/other/orders/o-1", Event: `{"v":1}`})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || len(svc.published) != 0 {
		t.Errorf("Publish() = %v reaching %d publishes, want invalid argument and none: a resource publishes under its own name", err, len(svc.published))
	}
}
