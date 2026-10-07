package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/descriptorpb"

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
