package proxy_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
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

func countingServer(t *testing.T, answer func(attempt int64, w http.ResponseWriter)) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		answer(attempts.Add(1), w)
	}))
	t.Cleanup(server.Close)
	return server, &attempts
}

func TestAPublishThrottledWith429IsSentAgainUntilItLands(t *testing.T) {
	t.Parallel()

	server, attempts := countingServer(t, func(attempt int64, w http.ResponseWriter) {
		if attempt < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := publish(proxy.NewService(bindRealtime(t, gatewayProperties(newKey(t))), proxy.NewGatewayTransport(server.Client(), server.URL+"/publish"))); err != nil {
		t.Fatalf("Publish() = %v, want it to land once the throttling passes", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("the gateway was sent the publish %d times, want 3", got)
	}
}

func TestAPublishTheGatewayAnsweredOtherThan429IsSentOnce(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusUnauthorized} {
		server, attempts := countingServer(t, func(_ int64, w http.ResponseWriter) { w.WriteHeader(status) })

		if err := publish(proxy.NewService(bindRealtime(t, gatewayProperties(newKey(t))), proxy.NewGatewayTransport(server.Client(), server.URL+"/publish"))); err == nil {
			t.Errorf("status %d: Publish() = nil, want it failed", status)
		}
		if got := attempts.Load(); got != 1 {
			t.Errorf("status %d: the gateway was sent the publish %d times, want once: it may have delivered it", status, got)
		}
	}
}

func TestAPublishWhoseConnectionCouldNotBeOpenedIsSentAgain(t *testing.T) {
	t.Parallel()

	server, attempts := countingServer(t, func(_ int64, w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) })
	var dials atomic.Int64
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if dials.Add(1) < 3 {
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}}

	if err := publish(proxy.NewService(bindRealtime(t, gatewayProperties(newKey(t))), proxy.NewGatewayTransport(client, server.URL+"/publish"))); err != nil {
		t.Fatalf("Publish() = %v, want it to land once a connection opens", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("the gateway was sent the publish %d times, want once", got)
	}
}

func TestAPublishWhoseConnectionDropsAfterItWasSentIsNotSentAgain(t *testing.T) {
	t.Parallel()

	server, attempts := countingServer(t, func(_ int64, w http.ResponseWriter) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})

	err := publish(proxy.NewService(bindRealtime(t, gatewayProperties(newKey(t))), proxy.NewGatewayTransport(server.Client(), server.URL+"/publish")))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("Publish() = %v, want unavailable", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("the gateway was sent the publish %d times, want once: it may have delivered it", got)
	}
}

func TestAPublishThrottledThroughoutFailsAsResourceExhaustedWithinThePublishTimeout(t *testing.T) {
	t.Parallel()

	server, attempts := countingServer(t, func(_ int64, w http.ResponseWriter) { w.WriteHeader(http.StatusTooManyRequests) })

	started := time.Now()
	err := publish(proxy.NewService(bindRealtime(t, gatewayProperties(newKey(t))), proxy.NewGatewayTransport(server.Client(), server.URL+"/publish")))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("Publish() = %v, want resource exhausted", err)
	}
	if took := time.Since(started); took > proxy.PublishTimeout {
		t.Errorf("Publish() took %s, want it to give up within %s", took, proxy.PublishTimeout)
	}
	if got := attempts.Load(); got < 2 {
		t.Errorf("the gateway was sent the publish %d times, want it sent again after a 429", got)
	}
}

func countingDials(t *testing.T, dial func(ctx context.Context, network, address string) (net.Conn, error)) (*http.Transport, *atomic.Int64) {
	t.Helper()
	var dials atomic.Int64
	return &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		return dial(ctx, network, address)
	}}, &dials
}

func TestAPublishToAHostThatDoesNotResolveIsSentOnce(t *testing.T) {
	t.Parallel()

	transport, dials := countingDials(t, func(_ context.Context, network, address string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: network, Err: &net.DNSError{Err: "no such host", Name: address, IsNotFound: true}}
	})

	if err := publish(proxy.NewService(bindRealtime(t, gatewayProperties(newKey(t))), proxy.NewGatewayTransport(&http.Client{Transport: transport}, "https://realtime.shop.example/publish"))); err == nil {
		t.Fatal("Publish() = nil, want it failed")
	}
	if got := dials.Load(); got != 1 {
		t.Errorf("the publish dialled %d times, want once: a host that does not exist will not exist a moment later", got)
	}
}

func TestAPublishRefusedByCertificateVerificationIsSentOnce(t *testing.T) {
	t.Parallel()

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	transport, dials := countingDials(t, (&net.Dialer{}).DialContext)

	if err := publish(proxy.NewService(bindRealtime(t, gatewayProperties(newKey(t))), proxy.NewGatewayTransport(&http.Client{Transport: transport}, server.URL+"/publish"))); err == nil {
		t.Fatal("Publish() = nil, want it refused by a certificate nothing here trusts")
	}
	if got := dials.Load(); got != 1 {
		t.Errorf("the publish dialled %d times, want once: a certificate that failed verification fails it again", got)
	}
}

func TestAPublishThrottledWithRetryAfterIsSentAgainNoSoonerThanItSays(t *testing.T) {
	t.Parallel()

	server, attempts := countingServer(t, func(attempt int64, w http.ResponseWriter) {
		if attempt == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	started := time.Now()
	if err := publish(proxy.NewService(bindRealtime(t, gatewayProperties(newKey(t))), proxy.NewGatewayTransport(server.Client(), server.URL+"/publish"))); err != nil {
		t.Fatalf("Publish() = %v, want it to land once Retry-After passes", err)
	}
	if took := time.Since(started); took < time.Second {
		t.Errorf("Publish() was sent again after %s, want no sooner than the second Retry-After asked", took)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("the gateway was sent the publish %d times, want 2", got)
	}
}

func TestAPublishWhoseRetryAfterOutlastsThePublishTimeoutFailsAtOnce(t *testing.T) {
	t.Parallel()

	server, attempts := countingServer(t, func(_ int64, w http.ResponseWriter) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	})

	started := time.Now()
	err := publish(proxy.NewService(bindRealtime(t, gatewayProperties(newKey(t))), proxy.NewGatewayTransport(server.Client(), server.URL+"/publish")))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("Publish() = %v, want resource exhausted", err)
	}
	if took := time.Since(started); took > proxy.PublishTimeout/2 {
		t.Errorf("Publish() took %s, want it to give up at once: the gateway asked for longer than the publish may take", took)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("the gateway was sent the publish %d times, want once", got)
	}
}
