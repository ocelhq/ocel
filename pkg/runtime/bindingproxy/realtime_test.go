package bindingproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/realtime/v1/realtimev1connect"
)

type recordingRealtime struct {
	realtimev1connect.UnimplementedRealtimeServiceHandler
	published []*realtimev1.PublishRequest
	minted    []*realtimev1.MintRequest
}

func (r *recordingRealtime) Mint(_ context.Context, req *realtimev1.MintRequest) (*realtimev1.MintResponse, error) {
	r.minted = append(r.minted, req)
	return &realtimev1.MintResponse{}, nil
}

func (r *recordingRealtime) Publish(_ context.Context, req *realtimev1.PublishRequest) (*realtimev1.PublishResponse, error) {
	r.published = append(r.published, req)
	return &realtimev1.PublishResponse{}, nil
}

func serveRealtime(t *testing.T) (realtimev1connect.RealtimeServiceClient, *recordingRealtime) {
	t.Helper()
	svc := &recordingRealtime{}
	ts := httptest.NewServer(NewMux(Services{Realtime: svc}))
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

func newMintRequest(grants ...*realtimev1.Grant) *realtimev1.MintRequest {
	return &realtimev1.MintRequest{Realtime: "app", Subject: "user-1", Ttl: durationpb.New(time.Minute), Grants: grants}
}

func subscribeGrant(channel string) *realtimev1.Grant {
	return &realtimev1.Grant{Operation: realtimev1.Operation_OPERATION_SUBSCRIBE, Channel: channel}
}

func TestAMintOfAConnectAndSubscribesUnderTheResourceReachesTheRealtimeService(t *testing.T) {
	t.Parallel()

	client, svc := serveRealtime(t)
	_, err := client.Mint(context.Background(), newMintRequest(
		&realtimev1.Grant{Operation: realtimev1.Operation_OPERATION_CONNECT, Channel: "/app"},
		subscribeGrant("/app/orders/o-1"),
		subscribeGrant("/app/orders/*"),
	))
	if err != nil {
		t.Fatalf("Mint() = %v", err)
	}
	if len(svc.minted) != 1 || len(svc.minted[0].GetGrants()) != 3 {
		t.Errorf("the realtime service received %v, want the one mint of three grants", svc.minted)
	}
}

func TestAMintOutsideTheResourceOrPastItsBoundsIsRefusedBeforeItReachesTheService(t *testing.T) {
	t.Parallel()

	tooMany := make([]*realtimev1.Grant, 52)
	for i := range tooMany {
		tooMany[i] = subscribeGrant("/app/orders/o-1")
	}
	longTTL := newMintRequest(subscribeGrant("/app/orders/o-1"))
	longTTL.Ttl = durationpb.New(6 * time.Minute)
	noTTL := newMintRequest(subscribeGrant("/app/orders/o-1"))
	noTTL.Ttl = nil
	noSubject := newMintRequest(subscribeGrant("/app/orders/o-1"))
	noSubject.Subject = ""
	for name, req := range map[string]*realtimev1.MintRequest{
		"another resource's channel":    newMintRequest(subscribeGrant("/other/orders/o-1")),
		"a channel sharing its prefix":  newMintRequest(subscribeGrant("/application/orders/o-1")),
		"a connect to another resource": newMintRequest(&realtimev1.Grant{Operation: realtimev1.Operation_OPERATION_CONNECT, Channel: "/other"}),
		"a connect to a channel":        newMintRequest(&realtimev1.Grant{Operation: realtimev1.Operation_OPERATION_CONNECT, Channel: "/app/orders"}),
		"no operation":                  newMintRequest(&realtimev1.Grant{Channel: "/app/orders/o-1"}),
		"no grant":                      newMintRequest(),
		"more than 51 grants":           newMintRequest(tooMany...),
		"a ttl past five minutes":       longTTL,
		"no ttl":                        noTTL,
		"no subject":                    noSubject,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client, svc := serveRealtime(t)
			_, err := client.Mint(context.Background(), req)
			if connect.CodeOf(err) != connect.CodeInvalidArgument || len(svc.minted) != 0 {
				t.Errorf("Mint() = %v reaching %d mints, want invalid argument and none", err, len(svc.minted))
			}
		})
	}
}
