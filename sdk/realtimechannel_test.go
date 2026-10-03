package ocel_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

}

type appSyncPublish struct {
	Authorization string
	Token         string
	Body          struct {
		Channel string   `json:"channel"`
		Events  []string `json:"events"`
	}
}

func serveFakeAppSync(t *testing.T, name string, status int, answer string) *[]appSyncPublish {
	t.Helper()
	var received []appSyncPublish
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/event" {
			t.Errorf("AppSync was sent %s %s, want POST /event", r.Method, r.URL.Path)
		}
		publish := appSyncPublish{Authorization: r.Header.Get("Authorization"), Token: r.Header.Get("X-Amz-Security-Token")}
		if err := json.NewDecoder(r.Body).Decode(&publish.Body); err != nil {
			t.Errorf("AppSync was sent no publish body: %v", err)
		}
		received = append(received, publish)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(server.Close)
	previous := *ocel.RealtimeHTTPClient
	*ocel.RealtimeHTTPClient = server.Client()
	t.Cleanup(func() { *ocel.RealtimeHTTPClient = previous })
	t.Setenv("AWS_ACCESS_KEY_ID", "ASIAAPPROLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "role-secret")
	t.Setenv("AWS_SESSION_TOKEN", "role-session")
	t.Setenv("AWS_REGION", "eu-west-1")
	fixture := readRealtimeFixture(t)
	fixture.Realtime.Host = strings.TrimPrefix(server.URL, "https://")
	deliverRealtime(t, name, fixture)
	return &received
}

func TestPublishOnAppSyncPostsTheEnvelopeAsOneEventSignedWithTheAppsRole(t *testing.T) {
	received := serveFakeAppSync(t, "app", http.StatusOK, `{"successful":[{"identifier":"x","index":0}],"failed":[]}`)
	orders := ocel.Channel[orderEvent, orderParams](ocel.Realtime("app"), "orders/:orderId", ocel.ChannelSubscribePublic())

	if err := orders.Publish(context.Background(), orderParams{OrderID: "o1"}, orderEvent{Status: "shipped"}); err != nil {
		t.Fatal(err)
	}

	if len(*received) != 1 {
		t.Fatalf("AppSync received %d publishes, want 1", len(*received))
	}
	got := (*received)[0]
	if got.Body.Channel != "/app/orders/o1" || len(got.Body.Events) != 1 {
		t.Fatalf("AppSync received %+v, want one event on /app/orders/o1", got.Body)
	}
	var envelope struct {
		Channel string     `json:"ch"`
		Kind    string     `json:"kind"`
		Data    orderEvent `json:"data"`
	}
	if err := json.Unmarshal([]byte(got.Body.Events[0]), &envelope); err != nil || envelope.Channel != "/app/orders/o1" || envelope.Kind != "live" || envelope.Data.Status != "shipped" {
		t.Errorf("the event = %s, want the live envelope of the published body", got.Body.Events[0])
	}
	if !strings.HasPrefix(got.Authorization, "AWS4-HMAC-SHA256 Credential=ASIAAPPROLE/") || !strings.Contains(got.Authorization, "/eu-west-1/appsync/aws4_request") || got.Token != "role-session" {
		t.Errorf("Authorization = %q with token %q, want a SigV4 signature for appsync in eu-west-1 under the role's session", got.Authorization, got.Token)
	}
}

func TestPublishOnAppSyncFailsWhenAppSyncRefusesOrFailsTheEvent(t *testing.T) {
	for _, tc := range []struct {
		status int
		answer string
		want   string
	}{
		{http.StatusForbidden, `{"errors":[{"errorType":"UnauthorizedException"}]}`, "status 403"},
		{http.StatusOK, `{"successful":[],"failed":[{"identifier":"x","index":0,"code":400,"message":"too large"}]}`, "too large"},
	} {
		serveFakeAppSync(t, "app", tc.status, tc.answer)
		orders := ocel.Channel[orderEvent, orderParams](ocel.Realtime("app"), "orders/:orderId", ocel.ChannelSubscribePublic())
		if err := orders.Publish(context.Background(), orderParams{OrderID: "o1"}, orderEvent{}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("err = %v, want %q", err, tc.want)
		}
	}
}
