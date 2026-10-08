package providerserver_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/descriptorpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func forwardPortsRequest(names ...string) *contractv1.ForwardPortsRequest {
	return &contractv1.ForwardPortsRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Bindings:    names,
	}
}

func TestForwardPortsHandsBackThePublishedBindingPointedAtItsForwardAndHoldsItUntilTheCallerLeaves(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))
	var asked provider.PortForwardRequest
	held := make(chan context.Context, 1)
	closed := make(chan struct{})
	vendor.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(ctx context.Context, req provider.PortForwardRequest, _ progress.Log) ([]provider.PortForward, error) {
			asked = req
			held <- ctx
			return []provider.PortForward{{Binding: "orders", LocalAddress: "127.0.0.1:41234", Close: func() { close(closed) }}}, nil
		}
	})

	ctx, leave := context.WithCancel(context.Background())
	defer leave()
	stream, err := client.ForwardPorts(ctx, forwardPortsRequest("orders"))
	if err != nil {
		t.Fatalf("ForwardPorts() error = %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("ForwardPorts() sent nothing: %v", stream.Err())
	}
	forwarded := stream.Msg().GetResponse()

	if asked.Tier != environment.TierProduction {
		t.Errorf("the hook was asked for tier %q, want production: a provider that forwards through infrastructure of its own keeps one per tier", asked.Tier)
	}
	if len(asked.Bindings) != 1 || asked.Bindings[0].Properties[provider.PropertyHost] != "fake-host" {
		t.Errorf("the hook was asked for %+v, want the published orders binding of shop", asked)
	}
	if len(forwarded.GetBindings()) != 1 {
		t.Fatalf("ForwardPorts() handed back %d bindings, want orders alone", len(forwarded.GetBindings()))
	}
	got := forwarded.GetBindings()[0].GetPostgres()
	if got.GetHost() != "127.0.0.1" || got.GetPort() != 41234 || got.GetTlsServerName() != "fake-host" || got.GetPassword() != "fake-password" {
		t.Errorf("ForwardPorts() handed back orders on %s:%d verified as %q, want 127.0.0.1:41234 verified as fake-host with its credentials",
			got.GetHost(), got.GetPort(), got.GetTlsServerName())
	}
	if len(forwarded.GetUnforwarded()) != 0 {
		t.Errorf("ForwardPorts() left %v unforwarded, want none", forwarded.GetUnforwarded())
	}
	field := forwarded.ProtoReflect().Descriptor().Fields().ByName("bindings")
	if options, _ := field.Options().(*descriptorpb.FieldOptions); !options.GetDebugRedact() {
		t.Error("the forwarded bindings are not marked debug_redact, want them redacted wherever they are printed: they hold every credential")
	}

	hookCtx := <-held
	select {
	case <-hookCtx.Done():
		t.Fatal("the forwards closed while the caller still held the stream")
	case <-time.After(50 * time.Millisecond):
	}
	leave()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the forwards stayed open after the caller left, want each one closed before the call returns")
	}
}

func TestForwardPortsEndsTheStreamWithTheFailureTheProviderReportsAndClosesTheForwards(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))
	fail := make(chan func(error), 1)
	closed := make(chan struct{})
	vendor.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(_ context.Context, req provider.PortForwardRequest, _ progress.Log) ([]provider.PortForward, error) {
			fail <- req.ReportFailure
			return []provider.PortForward{{Binding: "orders", LocalAddress: "127.0.0.1:41234", Close: func() { close(closed) }}}, nil
		}
	})

	stream, err := client.ForwardPorts(context.Background(), forwardPortsRequest("orders"))
	if err != nil {
		t.Fatalf("ForwardPorts() error = %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("ForwardPorts() sent nothing: %v", stream.Err())
	}
	(<-fail)(errors.New("the bastion task stopped: Task stopped by user"))

	for stream.Receive() {
		t.Errorf("ForwardPorts() sent a second response, %d bindings and unforwarded %v, after the forwards failed", len(stream.Msg().GetResponse().GetBindings()), stream.Msg().GetResponse().GetUnforwarded())
	}
	if err := stream.Err(); err == nil || !strings.Contains(err.Error(), "the bastion task stopped") {
		t.Errorf("ForwardPorts() stream ended with %v, want the failure the provider reported", err)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the forwards stayed open after they failed")
	}
}

func TestForwardPortsEndsTheStreamWithAFailureBeforeItsForwardsFinishClosingAndHoldsADeployUntilTheyHave(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))
	release := make(chan struct{})
	var released sync.Once
	releaseForwards := func() { released.Do(func() { close(release) }) }
	defer releaseForwards()
	vendor.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(_ context.Context, req provider.PortForwardRequest, _ progress.Log) ([]provider.PortForward, error) {
			req.ReportFailure(errors.New("the bastion task stopped: Task stopped by user"))
			return []provider.PortForward{{Binding: "orders", LocalAddress: "127.0.0.1:41234", Close: func() { <-release }}}, nil
		}
	})

	stream, err := client.ForwardPorts(context.Background(), forwardPortsRequest("orders"))
	if err != nil {
		t.Fatalf("ForwardPorts() error = %v", err)
	}
	ended := make(chan error, 1)
	go func() {
		for stream.Receive() {
		}
		ended <- stream.Err()
	}()
	select {
	case err := <-ended:
		if err == nil || !strings.Contains(err.Error(), "the bastion task stopped") {
			t.Fatalf("ForwardPorts() stream ended with %v, want the failure the provider reported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream waited for the failed forwards to close before saying they failed, want the failure first, so a caller that leaves meanwhile still hears it")
	}

	deployed := make(chan struct{})
	go func() {
		defer close(deployed)
		deploying, err := client.Deploy(context.Background(), deployRequest())
		if err != nil {
			return
		}
		for deploying.Receive() {
		}
		deploying.Close()
	}()
	select {
	case <-deployed:
		t.Fatal("the deploy ran while the provider was still closing the failed forwards, want it held until they are closed")
	case <-time.After(time.Second):
	}
	releaseForwards()
	select {
	case <-deployed:
	case <-time.After(30 * time.Second):
		t.Fatal("the deploy never ran once the failed forwards were closed")
	}
}

func TestForwardPortsOnAProviderThatCannotForwardHandsEveryBindingBackUnforwarded(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))

	stream, err := client.ForwardPorts(context.Background(), forwardPortsRequest("orders"))
	if err != nil {
		t.Fatalf("ForwardPorts() error = %v", err)
	}
	var responses []*contractv1.ForwardPortsResponse
	for stream.Receive() {
		responses = append(responses, stream.Msg().GetResponse())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("ForwardPorts() stream error = %v", err)
	}
	if len(responses) != 1 || len(responses[0].GetBindings()) != 0 || !slices.Equal(responses[0].GetUnforwarded(), []string{"orders"}) {
		t.Errorf("ForwardPorts() sent %d responses, want one naming orders unforwarded, and the stream closed", len(responses))
	}
}

func TestForwardPortsRefusesABindingNothingPublished(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))

	stream, err := client.ForwardPorts(context.Background(), forwardPortsRequest("missing"))
	if err != nil {
		t.Fatalf("ForwardPorts() error = %v", err)
	}
	for stream.Receive() {
		t.Errorf("ForwardPorts() forwarded %d bindings and left %v unforwarded, want a refusal", len(stream.Msg().GetResponse().GetBindings()), stream.Msg().GetResponse().GetUnforwarded())
	}
	if err := stream.Err(); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("ForwardPorts() error = %v, want a refusal naming missing", err)
	}
}

func TestForwardPortsLeavesAPostgresBindingAddressedByAURLUnforwarded(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	if result, _ := deploy(t, client, inlinePostgresRequest(servePostgres(t, "170004", 0).url(), "17")); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want the inline record published", result.GetError())
	}
	var asked []provider.Binding
	vendor.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(_ context.Context, req provider.PortForwardRequest, _ progress.Log) ([]provider.PortForward, error) {
			asked = req.Bindings
			forwards := make([]provider.PortForward, 0, len(req.Bindings))
			for _, binding := range req.Bindings {
				forwards = append(forwards, provider.PortForward{Binding: binding.Name, LocalAddress: "127.0.0.1:41234"})
			}
			return forwards, nil
		}
	})

	stream, err := client.ForwardPorts(context.Background(), forwardPortsRequest(inlineOrders))
	if err != nil {
		t.Fatalf("ForwardPorts() error = %v", err)
	}
	var responses []*contractv1.ForwardPortsResponse
	for stream.Receive() {
		responses = append(responses, stream.Msg().GetResponse())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("ForwardPorts() stream error = %v", err)
	}
	if len(asked) != 0 {
		t.Errorf("the hook was asked to forward %v, want nothing: a client connects by the url, which a forward cannot carry the server name of", asked)
	}
	if len(responses) != 1 || len(responses[0].GetBindings()) != 0 || !slices.Equal(responses[0].GetUnforwarded(), []string{inlineOrders}) {
		t.Errorf("ForwardPorts() sent %d responses, want one leaving %s unforwarded", len(responses), inlineOrders)
	}
}

func TestADeployAfterForwardsBeginsOnlyOnceTheProviderHasClosedThem(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))
	release := make(chan struct{})
	var released sync.Once
	releaseForwards := func() { released.Do(func() { close(release) }) }
	defer releaseForwards()
	vendor.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(context.Context, provider.PortForwardRequest, progress.Log) ([]provider.PortForward, error) {
			return []provider.PortForward{{Binding: "orders", LocalAddress: "127.0.0.1:41234", Close: func() { <-release }}}, nil
		}
	})

	ctx, leave := context.WithCancel(context.Background())
	stream, err := client.ForwardPorts(ctx, forwardPortsRequest("orders"))
	if err != nil {
		t.Fatalf("ForwardPorts() error = %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("ForwardPorts() sent nothing: %v", stream.Err())
	}
	leave()
	stream.Close()

	deployed := make(chan struct{})
	go func() {
		defer close(deployed)
		deploying, err := client.Deploy(context.Background(), deployRequest())
		if err != nil {
			return
		}
		for deploying.Receive() {
		}
		deploying.Close()
	}()
	select {
	case <-deployed:
		t.Fatal("the deploy ran while the provider was still closing the forwards, want it held until they are closed")
	case <-time.After(time.Second):
	}
	releaseForwards()
	select {
	case <-deployed:
	case <-time.After(30 * time.Second):
		t.Fatal("the deploy never ran once the forwards were closed")
	}
}

func TestWhatTheProviderSaysWhileOpeningForwardsAndWhileTheyAreHeldReachesTheCaller(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))
	opened := make(chan progress.Log, 1)
	vendor.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(_ context.Context, _ provider.PortForwardRequest, progress progress.Log) ([]provider.PortForward, error) {
			progress.Say("Creating the bastion")
			opened <- progress
			return []provider.PortForward{{Binding: "orders", LocalAddress: "127.0.0.1:41234"}}, nil
		}
	})

	ctx, leave := context.WithCancel(context.Background())
	defer leave()
	stream, err := client.ForwardPorts(ctx, forwardPortsRequest("orders"))
	if err != nil {
		t.Fatalf("ForwardPorts() error = %v", err)
	}
	var said []string
	for stream.Receive() {
		if event := stream.Msg().GetProgress(); event != nil {
			said = append(said, event.GetLevel().String()+" "+event.GetMessage())
			continue
		}
		if stream.Msg().GetResponse() == nil {
			t.Fatal("ForwardPorts() sent an event that is neither progress nor the forwards")
		}
		(<-opened).Warn("A connection to orders failed")
		if !stream.Receive() || stream.Msg().GetProgress() == nil {
			t.Fatalf("ForwardPorts() sent nothing once the forwards were open: %v", stream.Err())
		}
		said = append(said, stream.Msg().GetProgress().GetLevel().String()+" "+stream.Msg().GetProgress().GetMessage())
		break
	}

	if want := []string{"LEVEL_INFO Creating the bastion", "LEVEL_WARN A connection to orders failed"}; !slices.Equal(said, want) {
		t.Errorf("the caller heard %q, want %q", said, want)
	}
}

func TestWhatTheProviderSaysOnceItsForwardsAreClosedGoesNowhere(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t))
	served := &lateWrites{returned: make(chan struct{})}
	server := httptest.NewServer(served.watching(providerserver.ConformanceMux(providerserver.Config{
		Version: "1.0.0",
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return vendor, nil },
	})))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), configureInWorkingDir(t)); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	bootstrappedOverRPC(t, client)
	provisionedInfra(t, client, infraRequest(deployRequest()))
	opened := make(chan progress.Log, 1)
	closed := make(chan struct{})
	vendor.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(_ context.Context, _ provider.PortForwardRequest, progress progress.Log) ([]provider.PortForward, error) {
			opened <- progress
			return []provider.PortForward{{Binding: "orders", LocalAddress: "127.0.0.1:41234", Close: func() { close(closed) }}}, nil
		}
	})

	ctx, leave := context.WithCancel(context.Background())
	stream, err := client.ForwardPorts(ctx, forwardPortsRequest("orders"))
	if err != nil {
		t.Fatalf("ForwardPorts() error = %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("ForwardPorts() sent nothing: %v", stream.Err())
	}
	said := <-opened
	served.watch()
	leave()
	stream.Close()
	<-closed
	deadline := time.After(5 * time.Second)
	for returned := false; !returned; {
		said.Warn("A connection to orders failed")
		select {
		case <-served.returned:
			returned = true
		case <-deadline:
			t.Fatal("ForwardPorts never returned after the caller left")
		case <-time.After(time.Millisecond):
		}
	}
	said.Warn("A connection to orders failed")

	if late := served.late.Load(); late != 0 {
		t.Errorf("ForwardPorts wrote %d times to its response after returning, want what the provider says then dropped", late)
	}
}

func TestWhatTheProviderSaysWhileClosingForwardsThatFailedGoesNowhere(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))
	closed := make(chan struct{})
	vendor.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(_ context.Context, req provider.PortForwardRequest, progress progress.Log) ([]provider.PortForward, error) {
			req.ReportFailure(errors.New("the bastion task stopped: Task stopped by user"))
			return []provider.PortForward{{Binding: "orders", LocalAddress: "127.0.0.1:41234", Close: func() {
				defer close(closed)
				for range 50 {
					progress.Warn("Closing the forward of orders")
					time.Sleep(time.Millisecond)
				}
			}}}, nil
		}
	})

	stream, err := client.ForwardPorts(context.Background(), forwardPortsRequest("orders"))
	if err != nil {
		t.Fatalf("ForwardPorts() error = %v", err)
	}
	for stream.Receive() {
		if progress := stream.Msg().GetProgress(); progress != nil {
			t.Errorf("ForwardPorts() sent %q while closing forwards that failed, want nothing once the failure ends the stream", progress.GetMessage())
		}
	}
	if err := stream.Err(); err == nil || !strings.Contains(err.Error(), "the bastion task stopped") {
		t.Errorf("ForwardPorts() stream ended with %v, want the failure the provider reported", err)
	}
	stream.Close()
	<-closed
}

type lateWrites struct {
	mutex    sync.Mutex
	watched  bool
	done     bool
	returned chan struct{}
	late     atomic.Int32
}

func (l *lateWrites) watch() {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.watched = true
}

func (l *lateWrites) watching(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/ForwardPorts") {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&lateWriter{ResponseWriter: w, watch: l}, r)
		l.mutex.Lock()
		defer l.mutex.Unlock()
		if l.watched && !l.done {
			l.done = true
			close(l.returned)
		}
	})
}

type lateWriter struct {
	http.ResponseWriter
	watch *lateWrites
}

func (w *lateWriter) Write(p []byte) (int, error) {
	w.watch.mutex.Lock()
	defer w.watch.mutex.Unlock()
	if w.watch.done {
		w.watch.late.Add(1)
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}

func (w *lateWriter) Flush() {
	w.watch.mutex.Lock()
	defer w.watch.mutex.Unlock()
	if w.watch.done {
		w.watch.late.Add(1)
		return
	}
	w.ResponseWriter.(http.Flusher).Flush()
}

func (w *lateWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func TestForwardPortsServesTheBindingProxyForBindingsNoPortReachesAndHoldsItUntilTheCallerLeaves(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(twoAppRequest()))
	var asked provider.BindingProxyRequest
	closed := make(chan struct{})
	vendor.WithHooks(func(h *provider.Hooks) {
		h.ServeBindingProxy = func(_ context.Context, req provider.BindingProxyRequest, _ progress.Log) (provider.BindingProxy, error) {
			asked = req
			return provider.BindingProxy{Address: "http://127.0.0.1:41999", SessionToken: "token-1", Close: func() { close(closed) }}, nil
		}
	})

	ctx, leave := context.WithCancel(context.Background())
	defer leave()
	stream, err := client.ForwardPorts(ctx, forwardPortsRequest("uploads"))
	if err != nil {
		t.Fatalf("ForwardPorts() error = %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("ForwardPorts() sent nothing: %v", stream.Err())
	}
	served := stream.Msg().GetResponse()

	if asked.Slug != "shop" || asked.Env != stackrecords.ProductionEnv || asked.Tier != environment.TierProduction {
		t.Errorf("the hook was asked to proxy for %q in %q at tier %q, want shop in production: its sessions and tasks live under that scope", asked.Slug, asked.Env, asked.Tier)
	}
	if len(asked.Bindings) != 1 || asked.Bindings[0].Name != "uploads" || asked.Bindings[0].Type != provider.BindingBucket {
		t.Errorf("the hook was asked to proxy %+v, want the published uploads bucket", asked.Bindings)
	}
	if got := served.GetBindingProxy(); got.GetAddress() != "http://127.0.0.1:41999" || got.GetSessionToken() != "token-1" {
		t.Errorf("ForwardPorts() answered the proxy %v, want the address and token the hook served", got)
	}
	if len(served.GetBindings()) != 1 || served.GetBindings()[0].GetName() != "uploads" || served.GetBindings()[0].GetBucket() == nil {
		t.Errorf("ForwardPorts() handed back %v, want the uploads bucket binding as published", served.GetBindings())
	}
	if len(served.GetUnforwarded()) != 0 {
		t.Errorf("ForwardPorts() left %v unforwarded, want none", served.GetUnforwarded())
	}
	field := served.ProtoReflect().Descriptor().Fields().ByName("binding_proxy")
	if options, _ := field.Options().(*descriptorpb.FieldOptions); !options.GetDebugRedact() {
		t.Error("the binding proxy is not marked debug_redact, want it redacted wherever it is printed: it holds the session token")
	}

	select {
	case <-closed:
		t.Fatal("the binding proxy closed while the caller still held the stream")
	case <-time.After(50 * time.Millisecond):
	}
	leave()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the binding proxy stayed open after the caller left")
	}
}

func TestForwardPortsOnAProviderThatServesNoBindingProxyLeavesProxiedBindingsUnforwarded(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	provisionedInfra(t, client, infraRequest(twoAppRequest()))

	stream, err := client.ForwardPorts(context.Background(), forwardPortsRequest("uploads"))
	if err != nil {
		t.Fatalf("ForwardPorts() error = %v", err)
	}
	var responses []*contractv1.ForwardPortsResponse
	for stream.Receive() {
		responses = append(responses, stream.Msg().GetResponse())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("ForwardPorts() stream error = %v", err)
	}
	if len(responses) != 1 || responses[0].GetBindingProxy() != nil || !slices.Equal(responses[0].GetUnforwarded(), []string{"uploads"}) {
		t.Errorf("ForwardPorts() sent %d responses, want one naming uploads unforwarded with no proxy", len(responses))
	}
}

func TestForwardPortsLeavesABucketAddressedByAnEndpointUnforwardedSinceNoVendorProxyServesIt(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	props := &bindingsv1.BucketProperties{Bucket: "acme", Endpoint: "https://s3.example.com", AccessKeyId: "AKID", SecretAccessKey: inlinePassword}
	if result, _ := deploy(t, client, inlineBucketRequest(&resourcesv1.BucketConfig{}, props)); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want the inline record published", result.GetError())
	}
	var asked []provider.Binding
	vendor.WithHooks(func(h *provider.Hooks) {
		h.ServeBindingProxy = func(_ context.Context, req provider.BindingProxyRequest, _ progress.Log) (provider.BindingProxy, error) {
			asked = req.Bindings
			return provider.BindingProxy{Address: "http://127.0.0.1:41999", SessionToken: "token-1"}, nil
		}
	})

	stream, err := client.ForwardPorts(context.Background(), forwardPortsRequest(inlineUploads))
	if err != nil {
		t.Fatalf("ForwardPorts() error = %v", err)
	}
	var responses []*contractv1.ForwardPortsResponse
	for stream.Receive() {
		responses = append(responses, stream.Msg().GetResponse())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("ForwardPorts() stream error = %v", err)
	}
	if len(asked) != 0 {
		t.Errorf("the hook was asked to proxy %v, want nothing: the vendor's proxy serves the vendor's own buckets, not a store the config addresses", asked)
	}
	if len(responses) != 1 || len(responses[0].GetBindings()) != 0 || !slices.Equal(responses[0].GetUnforwarded(), []string{inlineUploads}) {
		t.Errorf("ForwardPorts() sent %d responses, want one leaving %s unforwarded", len(responses), inlineUploads)
	}
}

func TestForwardPortsLeavesUnforwardedTheProxiedBindingsTheProxyReportsItDoesNotServe(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(twoAppRequest()))
	closed := make(chan struct{})
	vendor.WithHooks(func(h *provider.Hooks) {
		h.ServeBindingProxy = func(context.Context, provider.BindingProxyRequest, progress.Log) (provider.BindingProxy, error) {
			return provider.BindingProxy{Address: "http://127.0.0.1:41999", SessionToken: "token-1", Unserved: []string{"uploads"}, Close: func() { close(closed) }}, nil
		}
	})

	ctx, leave := context.WithCancel(context.Background())
	defer leave()
	stream, err := client.ForwardPorts(ctx, forwardPortsRequest("uploads"))
	if err != nil {
		t.Fatalf("ForwardPorts() error = %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("ForwardPorts() sent nothing: %v", stream.Err())
	}
	served := stream.Msg().GetResponse()
	if len(served.GetBindings()) != 0 || !slices.Equal(served.GetUnforwarded(), []string{"uploads"}) {
		t.Errorf("ForwardPorts() handed back %v and left %v unforwarded, want uploads unforwarded since the proxy does not serve it", served.GetBindings(), served.GetUnforwarded())
	}
}
