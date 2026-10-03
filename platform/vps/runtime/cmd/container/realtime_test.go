package main

import (
	"context"
	"crypto/ed25519"
	"io"
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
	variables "github.com/ocelhq/ocel/platform/vps/provider/live"
)

func serveRealtimeProxy(t *testing.T, gatewayPublishURL string) realtimev1connect.RealtimeServiceClient {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
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
		Bindings: []live.Binding{{Name: "realtime--app", Key: "OCEL_RESOURCE_REALTIME_app", Type: bindingsv1.BindingType_BINDING_TYPE_REALTIME}},
		Realtime: gatewayPublishURL,
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

func TestTheRuntimePublishesARealtimeEventToTheBoxsGateway(t *testing.T) {
	var path, authorization, body string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		path, authorization, body = r.URL.Path, r.Header.Get("Authorization"), string(raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(gateway.Close)

	client := serveRealtimeProxy(t, gateway.URL+"/publish")
	event := `{"v":1,"id":"0123456789abcdef0123456789abcdef","ch":"/app/status","ts":1,"kind":"live","data":{}}`
	if _, err := client.Publish(context.Background(), &realtimev1.PublishRequest{Realtime: "app", Channel: "/app/status", Event: event}); err != nil {
		t.Fatalf("Publish() = %v", err)
	}
	if path != "/publish" || body != event || !strings.HasPrefix(authorization, "Bearer ") {
		t.Errorf("the gateway was sent %s to %s under %q, want the event to /publish under a bearer token", body, path, authorization)
	}
}
