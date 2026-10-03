package proxy_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/platform/realtime/gateway"
	"github.com/ocelhq/ocel/platform/realtime/proxy"
	"github.com/ocelhq/ocel/platform/realtime/token"
)

const (
	channel = "/app/orders/o-1"
	event   = `{"v":1,"id":"0123456789abcdef0123456789abcdef","ch":"/app/orders/o-1","ts":1,"kind":"live","data":{"status":"shipped"}}`
)

type records map[string]string

func (r records) Value(key string) string { return r[key] }

func bindRealtime(t *testing.T, properties *bindingsv1.RealtimeProperties) records {
	t.Helper()
	raw, err := protojson.Marshal(&bindingsv1.Binding{Name: "realtime--app", Properties: &bindingsv1.Binding_Realtime{Realtime: properties}})
	if err != nil {
		t.Fatal(err)
	}
	return records{"OCEL_RESOURCE_REALTIME_app": string(raw)}
}

func gatewayProperties(key ed25519.PrivateKey) *bindingsv1.RealtimeProperties {
	return &bindingsv1.RealtimeProperties{
		Transport:  bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_OCEL_GATEWAY,
		Url:        "wss://realtime.shop.example/event/realtime",
		Host:       "realtime.shop.example",
		SigningKey: key.Seed(),
		VerifyKey:  key.Public().(ed25519.PublicKey),
	}
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func publish(service *proxy.Service) error {
	_, err := service.Publish(context.Background(), &realtimev1.PublishRequest{Realtime: "app", Channel: channel, Event: event})
	return err
}

func TestAPublishIsTakenByTheGatewayTheBindingNames(t *testing.T) {
	t.Parallel()

	key := newKey(t)
	properties := gatewayProperties(key)
	server := httptest.NewServer(gateway.New(gateway.Config{
		Host: properties.GetHost(),
		Keys: func(namespace string) (ed25519.PublicKey, bool) {
			return key.Public().(ed25519.PublicKey), namespace == "app"
		},
	}))
	t.Cleanup(server.Close)

	service := proxy.NewService(bindRealtime(t, properties), proxy.NewGatewayTransport(server.Client(), server.URL+gateway.PublishPath))
	if err := publish(service); err != nil {
		t.Errorf("Publish() = %v, want the gateway serving the binding's host to take a publish sent to its internal address", err)
	}
}

func TestAGatewayPublishCarriesTheEventUnderAServerTokenForItsChannel(t *testing.T) {
	t.Parallel()

	key := newKey(t)
	var authorization, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	publishURL := server.URL + "/publish"

	if err := publish(proxy.NewService(bindRealtime(t, gatewayProperties(key)), proxy.NewGatewayTransport(server.Client(), publishURL))); err != nil {
		t.Fatalf("Publish() = %v", err)
	}
	if body != event {
		t.Errorf("the gateway was sent %s, want the event as the app encoded it", body)
	}
	if _, err := token.Verify(strings.TrimPrefix(authorization, "Bearer "), key.Public().(ed25519.PublicKey), time.Now(), token.Expected{
		Audience: "realtime.shop.example", Namespace: "app", Operation: token.Publish, Channel: channel, Subject: "server",
	}); err != nil {
		t.Errorf("the publish carried %q, which does not verify as a server publish token for %s: %v", authorization, channel, err)
	}
}

func TestAPublishTheGatewayRefusesFailsNamingItsStatus(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	err := publish(proxy.NewService(bindRealtime(t, gatewayProperties(newKey(t))), proxy.NewGatewayTransport(server.Client(), server.URL+"/publish")))
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("Publish() = %v, want the refusal named by its status", err)
	}
}

func TestAPublishOnAResourceThisRuntimeHasNoBindingForIsAFailedPrecondition(t *testing.T) {
	t.Parallel()

	err := publish(proxy.NewService(records{}, proxy.NewGatewayTransport(http.DefaultClient, "http://127.0.0.1:1/publish")))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("Publish() = %v, want a failed precondition: nothing was bound to publish to", err)
	}
}

func TestAGatewayRuntimeRefusesABindingOverAnotherTransport(t *testing.T) {
	t.Parallel()

	properties := gatewayProperties(newKey(t))
	properties.Transport = bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_APPSYNC_EVENTS
	err := publish(proxy.NewService(bindRealtime(t, properties), proxy.NewGatewayTransport(http.DefaultClient, "http://127.0.0.1:1/publish")))
	var refused *connect.Error
	if !errors.As(err, &refused) || refused.Code() != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "APPSYNC") {
		t.Errorf("Publish() = %v, want a failed precondition naming the transport this runtime does not publish to", err)
	}
}

func TestAPublishTheGatewayRefusesFailsWithTheCodeItsStatusMeans(t *testing.T) {
	t.Parallel()

	for status, want := range map[int]connect.Code{
		http.StatusBadRequest:          connect.CodeInvalidArgument,
		http.StatusUnauthorized:        connect.CodePermissionDenied,
		http.StatusForbidden:           connect.CodePermissionDenied,
		http.StatusTooManyRequests:     connect.CodeResourceExhausted,
		http.StatusNotFound:            connect.CodeFailedPrecondition,
		http.StatusInternalServerError: connect.CodeUnavailable,
		http.StatusBadGateway:          connect.CodeUnavailable,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		t.Cleanup(server.Close)

		err := publish(proxy.NewService(bindRealtime(t, gatewayProperties(newKey(t))), proxy.NewGatewayTransport(server.Client(), server.URL+"/publish")))
		if connect.CodeOf(err) != want {
			t.Errorf("status %d: Publish() = %v, want %s", status, err, want)
		}
	}
}

func TestAPublishTheCallerCancelsMidRequestFailsAsCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		cancel()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	service := proxy.NewService(bindRealtime(t, gatewayProperties(newKey(t))), proxy.NewGatewayTransport(server.Client(), server.URL+"/publish"))
	_, err := service.Publish(ctx, &realtimev1.PublishRequest{Realtime: "app", Channel: channel, Event: event})
	if connect.CodeOf(err) != connect.CodeCanceled {
		t.Errorf("Publish() = %v, want canceled: the caller gave up, the gateway did not fail", err)
	}
}

func TestAPublishPastTheCallersDeadlineFailsAsDeadlineExceeded(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)
	service := proxy.NewService(bindRealtime(t, gatewayProperties(newKey(t))), proxy.NewGatewayTransport(server.Client(), server.URL+"/publish"))
	_, err := service.Publish(ctx, &realtimev1.PublishRequest{Realtime: "app", Channel: channel, Event: event})
	if connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Errorf("Publish() = %v, want deadline exceeded", err)
	}
}
