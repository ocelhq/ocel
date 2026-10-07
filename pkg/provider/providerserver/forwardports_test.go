package providerserver_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
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
		h.ForwardPorts = func(ctx context.Context, req provider.PortForwardRequest) ([]provider.PortForward, error) {
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
	forwarded := stream.Msg()

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
		h.ForwardPorts = func(_ context.Context, req provider.PortForwardRequest) ([]provider.PortForward, error) {
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
		t.Errorf("ForwardPorts() sent a second response %v after the forwards failed", stream.Msg())
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
		responses = append(responses, stream.Msg())
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
		t.Errorf("ForwardPorts() forwarded %d bindings and left %v unforwarded, want a refusal", len(stream.Msg().GetBindings()), stream.Msg().GetUnforwarded())
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
		h.ForwardPorts = func(_ context.Context, req provider.PortForwardRequest) ([]provider.PortForward, error) {
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
		responses = append(responses, stream.Msg())
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
		h.ForwardPorts = func(context.Context, provider.PortForwardRequest) ([]provider.PortForward, error) {
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
