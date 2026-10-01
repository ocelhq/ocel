package ocel_test

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"ocel.dev"
)

var lowerHexID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type blobEvent struct {
	Data string `json:"data"`
}

func TestPublishPostsTheEnvelopeToTheGatewayWithAPublishTokenForItsChannel(t *testing.T) {
	gateway, fixture := serveFakeGateway(t, "app", http.StatusAccepted)
	resource := ocel.Realtime("app")
	orders := ocel.Channel[orderEvent, orderParams](resource, "orders/:orderId", ocel.ChannelSubscribePublic())

	if err := orders.Publish(context.Background(), orderParams{OrderID: "o_1"}, orderEvent{Status: "shipped"}); err != nil {
		t.Fatal(err)
	}

	events := gateway.listEvents()
	if len(events) != 1 {
		t.Fatalf("published = %d, want 1", len(events))
	}
	event := events[0]
	if event.Path != "/publish" || event.Envelope.Version != 1 || !lowerHexID.MatchString(event.Envelope.ID) || event.Envelope.Timestamp == 0 ||
		event.Envelope.Channel != "/app/orders/0zn5ptc" || event.Envelope.Kind != "live" || string(event.Envelope.Data) != `{"status":"shipped"}` {
		t.Errorf("published = %+v, want the envelope of the event on /app/orders/0zn5ptc", event)
	}
	claims := readClaims(t, fixture.Realtime.VerifyKey, strings.TrimPrefix(event.Authorization, "Bearer "))
	if claims.Issuer != "ocel:rt:app" || claims.Audience != fixture.Realtime.Host || claims.Subject != "server" ||
		claims.Ocel.Operation != "publish" || claims.Ocel.Channel != "/app/orders/0zn5ptc" || claims.Ocel.Namespace != "app" {
		t.Errorf("claims = %+v, want a server publish token for the channel", claims)
	}
}

func TestPublishRefusesParamsOrAnEventItCannotSend(t *testing.T) {
	gateway, _ := serveFakeGateway(t, "app", http.StatusAccepted)
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
	if events := gateway.listEvents(); len(events) != 0 {
		t.Errorf("published = %+v, want nothing", events)
	}
}

func TestPublishRefusesAnEventOutsideTheChannelSchema(t *testing.T) {
	gateway, _ := serveFakeGateway(t, "app", http.StatusAccepted)
	notes := ocel.Channel[chatMessage, struct{}](ocel.Realtime("app"), "notes", ocel.ChannelSubscribePublic(),
		ocel.Schema(`{"type":"object","properties":{"text":{"type":"string","maxLength":3}}}`))

	var refused *ocel.PublishRefusedError
	if err := notes.Publish(context.Background(), struct{}{}, chatMessage{Text: "toolong"}); !errors.As(err, &refused) || refused.Code != "invalid-body" {
		t.Errorf("err = %v, want refused for invalid-body", err)
	}
	if err := notes.Publish(context.Background(), struct{}{}, chatMessage{Text: "ok"}); err != nil {
		t.Fatal(err)
	}
	if events := gateway.listEvents(); len(events) != 1 || string(events[0].Envelope.Data) != `{"text":"ok"}` {
		t.Errorf("published = %+v, want only the short text", events)
	}
}

func TestPublishFailsWhenTheGatewayDoesNotAnswerInTime(t *testing.T) {
	gateway, _ := serveFakeGateway(t, "app", http.StatusAccepted)
	gateway.answerSlowly(10 * time.Second)
	shortenRealtimePublishTimeout(t)
	orders := ocel.Channel[orderEvent, orderParams](ocel.Realtime("app"), "orders/:orderId", ocel.ChannelSubscribePublic())

	started := time.Now()
	err := orders.Publish(context.Background(), orderParams{OrderID: "o1"}, orderEvent{})

	if err == nil || time.Since(started) > 5*time.Second {
		t.Errorf("err = %v after %s, want a failure at the deadline", err, time.Since(started))
	}
}

func TestPublishFailsWhenTheGatewayRefusesIt(t *testing.T) {
	serveFakeGateway(t, "app", http.StatusUnauthorized)
	orders := ocel.Channel[orderEvent, orderParams](ocel.Realtime("app"), "orders/:orderId", ocel.ChannelSubscribePublic())

	if err := orders.Publish(context.Background(), orderParams{OrderID: "o1"}, orderEvent{}); err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Errorf("err = %v, want the gateway's 401", err)
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

	deliverRealtime(t, "app", readRealtimeFixture(t))
	if err := orders.Publish(context.Background(), orderParams{OrderID: "o1"}, orderEvent{}); err == nil || !strings.Contains(err.Error(), "AppSync Events is not supported yet") {
		t.Errorf("err = %v, want AppSync publishing said to be unsupported yet", err)
	}
}
