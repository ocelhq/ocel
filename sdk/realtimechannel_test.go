package ocel_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"ocel.dev"
)

var lowerHexID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type blobEvent struct {
	Data string `json:"data"`
}

func TestPublishSendsTheEnvelopeToTheRuntimeOnItsWireChannel(t *testing.T) {
	runtime, _ := serveFakeRealtimeRuntime(t, "app")
	resource := ocel.Realtime("app")
	orders := ocel.Channel[orderEvent, orderParams](resource, "orders/:orderId", ocel.ChannelSubscribePublic())

	if err := orders.Publish(context.Background(), orderParams{OrderID: "o_1"}, orderEvent{Status: "shipped"}); err != nil {
		t.Fatal(err)
	}

	events := runtime.listEvents()
	if len(events) != 1 {
		t.Fatalf("published = %d, want 1", len(events))
	}
	event := events[0]
	if event.Realtime != "app" || event.Channel != "/app/orders/0zn5ptc" || event.Envelope.Version != 1 || !lowerHexID.MatchString(event.Envelope.ID) ||
		event.Envelope.Timestamp == 0 || event.Envelope.Channel != "/app/orders/0zn5ptc" || event.Envelope.Kind != "live" || string(event.Envelope.Data) != `{"status":"shipped"}` {
		t.Errorf("published = %+v, want the envelope of the event on /app/orders/0zn5ptc for app", event)
	}
}

func TestPublishRefusesParamsOrAnEventItCannotSend(t *testing.T) {
	runtime, _ := serveFakeRealtimeRuntime(t, "app")
	resource := ocel.Realtime("app")
	orders := ocel.Channel[orderEvent, orderParams](resource, "orders/:orderId", ocel.ChannelSubscribePublic())
	blobs := ocel.Channel[blobEvent, struct{}](resource, "blobs", ocel.ChannelSubscribePublic())

	for code, err := range map[string]error{
		"empty-value":    orders.Publish(context.Background(), orderParams{}, orderEvent{Status: "paid"}),
		"body-too-large": blobs.Publish(context.Background(), struct{}{}, blobEvent{Data: strings.Repeat("x", 240<<10)}),
	} {
		var refused *ocel.PublishRefusedError
		if !errors.As(err, &refused) || refused.Code != code {
			t.Errorf("err = %v, want refused for %s", err, code)
		}
	}
	if events := runtime.listEvents(); len(events) != 0 {
		t.Errorf("published = %+v, want nothing", events)
	}
}

func TestPublishRefusesAnEventOutsideTheChannelSchema(t *testing.T) {
	runtime, _ := serveFakeRealtimeRuntime(t, "app")
	notes := ocel.Channel[chatMessage, struct{}](ocel.Realtime("app"), "notes", ocel.ChannelSubscribePublic(),
		ocel.Schema(`{"type":"object","properties":{"text":{"type":"string","maxLength":3}}}`))

	var refused *ocel.PublishRefusedError
	if err := notes.Publish(context.Background(), struct{}{}, chatMessage{Text: "toolong"}); !errors.As(err, &refused) || refused.Code != "invalid-body" {
		t.Errorf("err = %v, want refused for invalid-body", err)
	}
	if err := notes.Publish(context.Background(), struct{}{}, chatMessage{Text: "ok"}); err != nil {
		t.Fatal(err)
	}
	if events := runtime.listEvents(); len(events) != 1 || string(events[0].Envelope.Data) != `{"text":"ok"}` {
		t.Errorf("published = %+v, want only the short text", events)
	}
}

func TestPublishFailsWhenTheRuntimeRefusesIt(t *testing.T) {
	runtime, _ := serveFakeRealtimeRuntime(t, "app")
	runtime.refuse(connect.NewError(connect.CodeUnavailable, errors.New("the gateway refused it with status 401")))
	orders := ocel.Channel[orderEvent, orderParams](ocel.Realtime("app"), "orders/:orderId", ocel.ChannelSubscribePublic())

	if err := orders.Publish(context.Background(), orderParams{OrderID: "o1"}, orderEvent{}); err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Errorf("err = %v, want the runtime's refusal", err)
	}
}

func TestPublishWithoutTheRuntimeSaysHowToReachIt(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	t.Setenv("OCEL_RUNTIME_ADDRESS", "")
	orders := ocel.Channel[orderEvent, orderParams](ocel.Realtime("app"), "orders/:orderId", ocel.ChannelSubscribePublic())

	if err := orders.Publish(context.Background(), orderParams{OrderID: "o1"}, orderEvent{}); err == nil || !strings.Contains(err.Error(), "OCEL_RUNTIME_ADDRESS") {
		t.Errorf("err = %v, want the missing runtime address named", err)
	}
}

func TestPublishOutsideAProvisionedRunSaysWhy(t *testing.T) {
	discoveryDeclarations(t)
	orders := ocel.Channel[orderEvent, orderParams](ocel.Realtime("app"), "orders/:orderId", ocel.ChannelSubscribePublic())
	var unprovisioned *ocel.UnprovisionedError
	if err := orders.Publish(context.Background(), orderParams{OrderID: "o1"}, orderEvent{}); !errors.As(err, &unprovisioned) {
		t.Errorf("err = %v, want UnprovisionedError during discovery", err)
	}

	t.Setenv("OCEL_PHASE", "")
	var missing *ocel.MissingBindingError
	if err := orders.Publish(context.Background(), orderParams{OrderID: "o1"}, orderEvent{}); !errors.As(err, &missing) || missing.Key != "OCEL_RESOURCE_REALTIME_app" {
		t.Errorf("err = %v, want OCEL_RESOURCE_REALTIME_app missing", err)
	}

}
