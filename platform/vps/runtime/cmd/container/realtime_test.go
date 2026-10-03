package main

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/realtime/v1/realtimev1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/platform/realtime/gateway"
	variables "github.com/ocelhq/ocel/platform/vps/provider/live"
)

func serveRealtimeProxy(t *testing.T, key ed25519.PrivateKey, gatewayPublishURL string) realtimev1connect.RealtimeServiceClient {
	t.Helper()
	record, err := protojson.Marshal(&bindingsv1.Binding{Name: "realtime--app", Properties: &bindingsv1.Binding_Realtime{Realtime: &bindingsv1.RealtimeProperties{
		Transport:  bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_OCEL_GATEWAY,
		Url:        "wss://shop.example/event/realtime",
		Host:       "shop.example",
		SigningKey: key.Seed(),
		VerifyKey:  key.Public().(ed25519.PublicKey),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := variables.Manifest{
		Slug: "shop", Tier: "production",
		Bindings:           []live.Binding{{Name: "realtime--app", Key: "OCEL_RESOURCE_REALTIME_app", Type: bindingsv1.BindingType_BINDING_TYPE_REALTIME}},
		RealtimePublishURL: gatewayPublishURL,
	}
	rendered, err := variables.Render(manifest)
	if err != nil {
		t.Fatal(err)
	}
	socket := answering(t, map[string]string{"OCEL_RESOURCE_REALTIME_app": string(record)})
	values, err := resolve(context.Background(), string(rendered), socket, filepath.Join(t.TempDir(), "live"))
	if err != nil {
		t.Fatalf("resolve() = %v", err)
	}
	served, err := proxying(manifest, values, socket, "127.0.0.1:1")
	if err != nil {
		t.Fatalf("proxying() = %v", err)
	}
	t.Cleanup(func() { _ = served.Close() })
	return realtimev1connect.NewRealtimeServiceClient(http.DefaultClient, readEnv(served, processenv.RuntimeAddressEnvVar),
		connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set("Authorization", localrpc.FormatAuthHeader(readEnv(served, localrpc.SessionTokenEnvVar)))
				return next(ctx, req)
			}
		})))
}

func readEnv(served bindingproxy.Served, key string) string {
	for _, entry := range served.Env {
		if name, value, _ := strings.Cut(entry, "="); name == key {
			return value
		}
	}
	return ""
}

func TestTheRuntimePublishesARealtimeEventToTheBoxsGatewayAtItsInternalAddress(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	box := httptest.NewServer(gateway.New(gateway.Config{
		Host: "shop.example",
		Keys: func(namespace string) (ed25519.PublicKey, bool) {
			return key.Public().(ed25519.PublicKey), namespace == "app"
		},
	}))
	t.Cleanup(box.Close)

	client := serveRealtimeProxy(t, key, box.URL+gateway.PublishPath)
	event := `{"v":1,"id":"0123456789abcdef0123456789abcdef","ch":"/app/status","ts":1,"kind":"live","data":{}}`
	if _, err := client.Publish(context.Background(), &realtimev1.PublishRequest{Realtime: "app", Channel: "/app/status", Event: event}); err != nil {
		t.Errorf("Publish() = %v, want the gateway serving shop.example to take a publish sent to its internal address", err)
	}
}
