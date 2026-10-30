package providerserver

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestServeNeedsAConstructor(t *testing.T) {
	identity, err := localrpc.NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity() error = %v", err)
	}
	t.Setenv(localrpc.ClientCertEnvVar, identity.CertificatePEM())

	if err := Serve(Config{Version: "test", ExportSpans: exportNoSpans}); err == nil {
		t.Fatal("Serve() with no constructor returned nil, want a refusal to start")
	}
}

func TestServeNeedsASpanExporter(t *testing.T) {
	identity, err := localrpc.NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity() error = %v", err)
	}
	t.Setenv(localrpc.ClientCertEnvVar, identity.CertificatePEM())

	err = Serve(Config{Version: "test", New: func(context.Context, provider.Settings) (provider.Provider, error) { return stubProvider{}, nil }})
	if err == nil || !strings.Contains(err.Error(), "ExportSpans") {
		t.Fatalf("Serve() with no span exporter = %v, want a refusal to start that names it", err)
	}
}

func exportNoSpans(ctx context.Context, _ string) (context.Context, func(context.Context) error, error) {
	return ctx, func(context.Context) error { return nil }, nil
}

func TestServeNeedsTheClientCertificate(t *testing.T) {
	for _, tc := range []struct {
		name string
		cert string
	}{
		{"nothing at all", ""},
		{"something that is not a certificate", "not-a-pem-block"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(localrpc.ClientCertEnvVar, tc.cert)

			err := Serve(Config{Version: "test", ExportSpans: exportNoSpans, New: func(context.Context, provider.Settings) (provider.Provider, error) { return nil, nil }})
			if err == nil || !strings.Contains(err.Error(), localrpc.ClientCertEnvVar) {
				t.Fatalf("Serve() error = %v, want it to name the environment variable the CLI must set", err)
			}
		})
	}
}

func TestSessionConstructsTheProviderExactlyOnce(t *testing.T) {
	t.Parallel()

	var built int
	s := &session{config: Config{New: func(context.Context, provider.Settings) (provider.Provider, error) {
		built++
		return stubProvider{}, nil
	}}}

	if err := s.configure(context.Background(), provider.Settings{ProjectDir: t.TempDir()}); err != nil {
		t.Fatalf("configure() error = %v", err)
	}
	err := s.configure(context.Background(), provider.Settings{ProjectDir: t.TempDir()})
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("a second configure: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}
	if built != 1 {
		t.Errorf("the constructor ran %d times, want exactly once per session", built)
	}
}

func TestSessionRefusesEveryRPCBeforeConfigure(t *testing.T) {
	t.Parallel()

	s := &session{config: Config{New: func(context.Context, provider.Settings) (provider.Provider, error) { return stubProvider{}, nil }}}

	_, err := s.use()
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("use() before configure: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}

	if err := s.configure(context.Background(), provider.Settings{ProjectDir: t.TempDir()}); err != nil {
		t.Fatalf("configure() error = %v", err)
	}
	if _, err := s.use(); err != nil {
		t.Fatalf("use() after configure: error = %v", err)
	}
}

func TestSessionTurnsARefusedConstructionIntoInvalidArgument(t *testing.T) {
	t.Parallel()

	s := &session{config: Config{New: func(context.Context, provider.Settings) (provider.Provider, error) {
		return nil, refusal.Refuse(refusal.CodeInvalid, "unknown option \"regoin\"")
	}}}

	err := s.configure(context.Background(), provider.Settings{ProjectDir: t.TempDir(), Options: provider.Options{"regoin": "typo"}})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("configure() with refused options: code = %v, want %v", got, connect.CodeInvalidArgument)
	}
	if _, err := s.use(); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Error("a refused Configure left the session usable, want it still unconfigured")
	}
}

func TestSessionReportsAFailedConstructionAsAFailure(t *testing.T) {
	t.Parallel()

	s := &session{config: Config{New: func(context.Context, provider.Settings) (provider.Provider, error) {
		return nil, errors.New("the vendor sdk is unreachable")
	}}}

	if got := connect.CodeOf(s.configure(context.Background(), provider.Settings{ProjectDir: t.TempDir()})); got != connect.CodeInternal {
		t.Fatalf("configure() with a failure: code = %v, want %v", got, connect.CodeInternal)
	}
}

func TestSessionRefusesAConstructorThatReturnsNothing(t *testing.T) {
	t.Parallel()

	s := &session{config: Config{New: func(context.Context, provider.Settings) (provider.Provider, error) { return nil, nil }}}

	if got := connect.CodeOf(s.configure(context.Background(), provider.Settings{ProjectDir: t.TempDir()})); got != connect.CodeInternal {
		t.Fatalf("configure() with a nil provider: code = %v, want %v", got, connect.CodeInternal)
	}
}

func TestSessionConfigureIsSafeUnderConcurrency(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var built int
	s := &session{config: Config{New: func(context.Context, provider.Settings) (provider.Provider, error) {
		mu.Lock()
		defer mu.Unlock()
		built++
		return stubProvider{}, nil
	}}}

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.configure(context.Background(), provider.Settings{ProjectDir: t.TempDir()})
		}()
	}
	wg.Wait()

	if built != 1 {
		t.Errorf("the constructor ran %d times under a concurrent Configure, want once", built)
	}
}

type stubProvider struct{ provider.Provider }

func (stubProvider) Facts() provider.Facts { return provider.Facts{Vendor: "stub"} }

func (stubProvider) Hooks() provider.Hooks { return provider.Hooks{} }

func TestServingLeavesTheStandardLibrarysDefaultTransportInPlace(t *testing.T) {
	identity, err := localrpc.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(localrpc.ClientCertEnvVar, identity.CertificatePEM())
	read, written, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin := os.Stdin
	os.Stdin = read
	t.Cleanup(func() {
		os.Stdin = stdin
		_ = read.Close()
	})
	_ = written.Close()

	if err := Serve(Config{Version: "test", ExportSpans: exportNoSpans, New: func(context.Context, provider.Settings) (provider.Provider, error) { return stubProvider{}, nil }}); err != nil {
		t.Fatalf("Serve() = %v", err)
	}
	if _, standard := http.DefaultTransport.(*http.Transport); !standard {
		t.Errorf("http.DefaultTransport is a %T after serving, want the standard library's *http.Transport that Google's credential code asserts", http.DefaultTransport)
	}
}

func TestStdinReachingEOFCancelsTheServeContext(t *testing.T) {
	t.Parallel()

	read, written, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	t.Cleanup(func() { _ = read.Close() })

	ctx := cancelWhenStdinCloses(context.Background(), read)
	select {
	case <-ctx.Done():
		t.Fatal("the context ended while the CLI still held stdin open")
	case <-time.After(100 * time.Millisecond):
	}

	_ = written.Close()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the context outlived the CLI closing stdin, want a provider that cannot outlive its CLI")
	}
}

func TestStdinThatIsNotAPipeLeavesTheServeContextRunning(t *testing.T) {
	t.Parallel()

	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = null.Close() })

	ctx := cancelWhenStdinCloses(context.Background(), null)
	select {
	case <-ctx.Done():
		t.Fatal("a stdin at EOF that no CLI holds ended the context, want only a pipe's EOF to count")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestServeLetsAnInFlightCallFinishItsCleanupBeforeReturning(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	var cleanedUp atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		time.Sleep(100 * time.Millisecond)
		cleanedUp.Store(true)
	})

	returned, stop, address := startServe(t, handler, 5*time.Second)
	go func() {
		resp, err := http.Get("http://" + address)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	<-started
	stop()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("serve() did not return once its context ended")
	}
	if !cleanedUp.Load() {
		t.Fatal("serve() returned before the cancelled call finished, want its lease releases to run before the provider exits")
	}
}

func TestServeReturnsWithinItsWindowWhenACallIgnoresCancellation(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		time.Sleep(30 * time.Second)
	})

	returned, stop, address := startServe(t, handler, 200*time.Millisecond)
	go func() {
		resp, err := http.Get("http://" + address)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	stop()

	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("serve() waited on a call that ignores cancellation, want it to return once its window passes")
	}
}

func TestACallServedCarriesTheTraceTheProviderWasStartedUnder(t *testing.T) {
	t.Parallel()

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	handed := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled}))
	seen := make(chan trace.SpanContext, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- trace.SpanContextFromContext(r.Context())
	})

	_, _, address := startServeUnder(t, handed, handler, time.Second)
	resp, err := http.Get("http://" + address)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if got := <-seen; got.TraceID() != traceID || got.SpanID() != spanID {
		t.Errorf("call context carries %s/%s, want the provider's handed parent so its spans reach the run's trace", got.TraceID(), got.SpanID())
	}
}

func startServe(t *testing.T, handler http.Handler, window time.Duration) (<-chan error, context.CancelFunc, string) {
	t.Helper()
	return startServeUnder(t, context.Background(), handler, window)
}

func startServeUnder(t *testing.T, base context.Context, handler http.Handler, window time.Duration) (<-chan error, context.CancelFunc, string) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	ctx, stop := context.WithCancel(base)
	t.Cleanup(stop)
	returned := make(chan error, 1)
	go func() { returned <- serve(ctx, ln, handler, window, func() {}) }()
	return returned, stop, ln.Addr().String()
}
