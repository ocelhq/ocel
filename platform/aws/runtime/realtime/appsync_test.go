package realtime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws/ratelimit"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"

	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/platform/aws/runtime/realtime"
)

const (
	apiHost = "abcdefghijklmnopqrstuvwxyz.appsync-api.eu-west-2.amazonaws.com"
	channel = "/app/orders/o-1"
	event   = `{"v":1,"id":"0123456789abcdef0123456789abcdef","ch":"/app/orders/o-1","ts":1,"kind":"live","data":{"status":"shipped"}}`
)

var staticCredentials = credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "session-token")

type received struct {
	header http.Header
	path   string
	body   []byte
}

type appSync struct {
	mu       sync.Mutex
	received []received
	answers  []func(http.ResponseWriter)
}

func (a *appSync) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.received = append(a.received, received{header: r.Header.Clone(), path: r.URL.Path, body: body})
	answer := func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"successful":[{"identifier":"1","index":0}],"failed":[]}`))
	}
	if len(a.answers) > 0 {
		answer, a.answers = a.answers[0], a.answers[1:]
	}
	a.mu.Unlock()
	answer(w)
}

func (a *appSync) taken() []received {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]received(nil), a.received...)
}

type toServer struct{ server *url.URL }

func (s toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	redirected := r.Clone(r.Context())
	redirected.URL.Scheme, redirected.URL.Host = s.server.Scheme, s.server.Host
	return http.DefaultTransport.RoundTrip(redirected)
}

func serveAppSync(t *testing.T, answers ...func(http.ResponseWriter)) (*appSync, realtime.Config) {
	t.Helper()
	endpoint := &appSync{answers: answers}
	server := httptest.NewServer(endpoint)
	t.Cleanup(server.Close)
	address, _ := url.Parse(server.URL)
	return endpoint, realtime.Config{
		Client:      &http.Client{Transport: toServer{server: address}},
		Credentials: staticCredentials,
		Retryer:     retry.NewStandard(func(o *retry.StandardOptions) { o.Backoff = noBackoff{} }),
	}
}

type noBackoff struct{}

func (noBackoff) BackoffDelay(int, error) (time.Duration, error) { return 0, nil }

func publish(cfg realtime.Config) error {
	return realtime.NewAppSyncTransport(cfg)(context.Background(), &bindingsv1.RealtimeProperties{
		Transport: bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_APPSYNC_EVENTS,
		Host:      apiHost,
	}, &realtimev1.PublishRequest{Realtime: "app", Channel: channel, Event: event})
}

func TestAPublishPostsTheEventToTheAPIHostsEventPath(t *testing.T) {
	t.Parallel()

	endpoint, cfg := serveAppSync(t)
	if err := publish(cfg); err != nil {
		t.Fatalf("publish = %v", err)
	}
	got := endpoint.taken()[0]
	var body struct {
		Channel string   `json:"channel"`
		Events  []string `json:"events"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("AppSync was sent %s, which is no JSON: %v", got.body, err)
	}
	if got.path != "/event" || body.Channel != channel || len(body.Events) != 1 || body.Events[0] != event {
		t.Errorf("AppSync was sent %s to %s, want {channel, events:[the envelope]} to /event", got.body, got.path)
	}
}

func TestAPublishIsSignedForAppSyncInTheRegionItsHostNames(t *testing.T) {
	t.Parallel()

	endpoint, cfg := serveAppSync(t)
	if err := publish(cfg); err != nil {
		t.Fatalf("publish = %v", err)
	}
	got := endpoint.taken()[0]
	signedAt, err := time.Parse("20060102T150405Z", got.header.Get("X-Amz-Date"))
	if err != nil {
		t.Fatalf("the publish carried X-Amz-Date %q: %v", got.header.Get("X-Amz-Date"), err)
	}
	want, _ := http.NewRequest(http.MethodPost, "https://"+apiHost+"/event", bytes.NewReader(got.body))
	want.Header.Set("Content-Type", "application/json")
	sum := sha256.Sum256(got.body)
	creds, _ := staticCredentials.Retrieve(context.Background())
	if err := v4.NewSigner().SignHTTP(context.Background(), creds, want, hex.EncodeToString(sum[:]), "appsync", "eu-west-2", signedAt); err != nil {
		t.Fatal(err)
	}
	if got.header.Get("Authorization") != want.Header.Get("Authorization") {
		t.Errorf("Authorization = %q, want %q: a SigV4 signature for appsync in eu-west-2 over this body", got.header.Get("Authorization"), want.Header.Get("Authorization"))
	}
	if got.header.Get("X-Amz-Security-Token") != "session-token" {
		t.Errorf("X-Amz-Security-Token = %q, want the role session's token", got.header.Get("X-Amz-Security-Token"))
	}
}

func TestAnEventAppSyncListsAsFailedFailsThePublish(t *testing.T) {
	t.Parallel()

	_, cfg := serveAppSync(t, func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"successful":[],"failed":[{"identifier":"1","index":0,"code":400,"message":"bad"}]}`))
	})
	if err := publish(cfg); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Errorf("publish = %v, want the failed event reported", err)
	}
}

func TestAPublishAppSyncThrottlesIsRetried(t *testing.T) {
	t.Parallel()

	endpoint, cfg := serveAppSync(t, func(w http.ResponseWriter) { w.WriteHeader(http.StatusTooManyRequests) })
	if err := publish(cfg); err != nil {
		t.Errorf("publish = %v, want the throttled publish retried until AppSync takes it", err)
	}
	if len(endpoint.taken()) != 2 {
		t.Errorf("AppSync was sent %d publishes, want the throttled one and its retry", len(endpoint.taken()))
	}
}

func TestAPublishAppSyncRefusesFailsNamingTheStatus(t *testing.T) {
	t.Parallel()

	_, cfg := serveAppSync(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"errorType":"UnauthorizedException"}]}`))
	})
	if err := publish(cfg); connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "403") {
		t.Errorf("publish = %v, want a permission denied naming the status", err)
	}
}

func TestAPublishAppSyncFindsMalformedFailsAsAnInvalidArgument(t *testing.T) {
	t.Parallel()

	_, cfg := serveAppSync(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"errorType":"BadRequestException"}]}`))
	})
	if err := publish(cfg); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("publish = %v, want an invalid argument", err)
	}
}

func TestAPublishTheCallerCancelsWhileAppSyncIsThrottlingFailsAsCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	_, cfg := serveAppSync(t, func(w http.ResponseWriter) {
		cancel()
		w.WriteHeader(http.StatusTooManyRequests)
	})
	cfg.Retryer = retry.NewStandard(func(o *retry.StandardOptions) { o.Backoff = constantBackoff(time.Minute) })
	err := realtime.NewAppSyncTransport(cfg)(ctx, &bindingsv1.RealtimeProperties{
		Transport: bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_APPSYNC_EVENTS,
		Host:      apiHost,
	}, &realtimev1.PublishRequest{Realtime: "app", Channel: channel, Event: event})
	if connect.CodeOf(err) != connect.CodeCanceled {
		t.Errorf("publish = %v, want canceled: the caller gave up while the publish waited to retry", err)
	}
}

type refusedDialOnce struct {
	mu      sync.Mutex
	refused bool
	next    http.RoundTripper
}

func (d *refusedDialOnce) RoundTrip(r *http.Request) (*http.Response, error) {
	d.mu.Lock()
	refuse := !d.refused
	d.refused = true
	d.mu.Unlock()
	if refuse {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	}
	return d.next.RoundTrip(r)
}

func TestAPublishWhoseConnectionWasRefusedIsRetried(t *testing.T) {
	t.Parallel()

	endpoint, cfg := serveAppSync(t)
	cfg.Client = &http.Client{Transport: &refusedDialOnce{next: cfg.Client.Transport}}
	if err := publish(cfg); err != nil {
		t.Errorf("publish = %v, want a publish that never reached AppSync sent again", err)
	}
	if len(endpoint.taken()) != 1 {
		t.Errorf("AppSync took %d publishes, want the retried one", len(endpoint.taken()))
	}
}

func TestAPublishWhoseConnectionDroppedAfterItWasSentIsNotSentTwice(t *testing.T) {
	t.Parallel()

	endpoint, cfg := serveAppSync(t, func(w http.ResponseWriter) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
	err := publish(cfg)
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("publish = %v, want unavailable", err)
	}
	if len(endpoint.taken()) != 1 {
		t.Errorf("AppSync was sent %d publishes, want one: AppSync may have delivered the event before the connection dropped", len(endpoint.taken()))
	}
}

func TestAThrottledPublishIsNotRetriedOnceTheRetryQuotaIsSpent(t *testing.T) {
	t.Parallel()

	endpoint, cfg := serveAppSync(t, func(w http.ResponseWriter) { w.WriteHeader(http.StatusTooManyRequests) })
	cfg.Retryer = retry.NewStandard(func(o *retry.StandardOptions) {
		o.Backoff = noBackoff{}
		o.RateLimiter = ratelimit.NewTokenRateLimit(0)
	})
	if err := publish(cfg); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("publish = %v, want resource exhausted", err)
	}
	if len(endpoint.taken()) != 1 {
		t.Errorf("AppSync was sent %d publishes, want one: the runtime's retry quota is spent", len(endpoint.taken()))
	}
}

type constantBackoff time.Duration

func (c constantBackoff) BackoffDelay(int, error) (time.Duration, error) {
	return time.Duration(c), nil
}

func TestAnAppSyncRuntimeRefusesABindingOverAnotherTransport(t *testing.T) {
	t.Parallel()

	_, cfg := serveAppSync(t)
	err := realtime.NewAppSyncTransport(cfg)(context.Background(), &bindingsv1.RealtimeProperties{
		Transport: bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_OCEL_GATEWAY,
		Host:      apiHost,
	}, &realtimev1.PublishRequest{Realtime: "app", Channel: channel, Event: event})
	if err == nil || !strings.Contains(err.Error(), "OCEL_GATEWAY") {
		t.Errorf("publish = %v, want a binding over the gateway refused", err)
	}
}

func TestAPublishAppSyncAnswersWithAThrottlingErrorIsRetried(t *testing.T) {
	t.Parallel()

	endpoint, cfg := serveAppSync(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"errorType":"ThrottlingException","message":"Rate exceeded"}]}`))
	})
	if err := publish(cfg); err != nil {
		t.Errorf("publish = %v, want the throttled publish retried until AppSync takes it", err)
	}
	if len(endpoint.taken()) != 2 {
		t.Errorf("AppSync was sent %d publishes, want the throttled one and its retry", len(endpoint.taken()))
	}
}

func TestAPublishAppSyncFailsWithAServerErrorIsNotSentTwice(t *testing.T) {
	t.Parallel()

	endpoint, cfg := serveAppSync(t, func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) })
	if err := publish(cfg); err == nil {
		t.Errorf("publish = nil, want the server error reported")
	}
	if len(endpoint.taken()) != 1 {
		t.Errorf("AppSync was sent %d publishes, want one: AppSync may have delivered an event it answered 500 for", len(endpoint.taken()))
	}
}
