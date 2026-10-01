package topics_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"connectrpc.com/connect"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

var ulid = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

type pulledMessage struct {
	AckID   string `json:"ackId"`
	Message struct {
		Data        string            `json:"data"`
		Attributes  map[string]string `json:"attributes"`
		MessageID   string            `json:"messageId"`
		PublishTime string            `json:"publishTime"`
		OrderingKey string            `json:"orderingKey"`
	} `json:"message"`
}

func (m pulledMessage) payload() string {
	raw, _ := base64.StdEncoding.DecodeString(m.Message.Data)
	return string(raw)
}

func (m pulledMessage) pushBody() []byte {
	body, _ := json.Marshal(map[string]any{"message": m.Message, "subscription": "pulled"})
	return body
}

type published struct {
	t          *testing.T
	endpoint   string
	deployment topics.Deployment
}

func newPublished(t *testing.T) published {
	t.Helper()
	endpoint := emulatedEndpoint(t)
	clients := liveClients(t)
	deployment := topics.Deployment{
		Clients:  clients,
		Names:    topics.Names{Namespace: "ocel", Scope: scopeOf(t)},
		Declared: deployedTopics(),
	}
	topology := topics.Topology{Names: deployment.Names, Topics: deployment.Declared, Pushes: map[string]topics.Push{"worker": {URL: "https://worker.run.app", ServiceAccount: invoker}}}
	if err := topology.Ensure(context.Background(), clients); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = topology.Remove(context.Background(), clients) })
	return published{t: t, endpoint: endpoint, deployment: deployment}
}

func (p published) pull(topic, consumer string) []pulledMessage {
	p.t.Helper()
	path := p.endpoint + "/v1/projects/" + p.deployment.Clients.Project + "/subscriptions/" + p.deployment.Names.Subscription(topic, consumer)
	resp, err := http.Post(path+":pull", "application/json", bytes.NewReader([]byte(`{"maxMessages":100,"returnImmediately":true}`)))
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	var pulled struct {
		ReceivedMessages []pulledMessage `json:"receivedMessages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pulled); err != nil {
		p.t.Fatal(err)
	}
	var ackIDs []string
	for _, message := range pulled.ReceivedMessages {
		ackIDs = append(ackIDs, message.AckID)
	}
	if len(ackIDs) > 0 {
		body, _ := json.Marshal(map[string]any{"ackIds": ackIDs})
		ack, err := http.Post(path+":acknowledge", "application/json", bytes.NewReader(body))
		if err == nil {
			_ = ack.Body.Close()
		}
	}
	return pulled.ReceivedMessages
}

func TestLiveASendPublishesOneMessageWithItsIdAndPayloadToTheTopic(t *testing.T) {
	p := newPublished(t)
	ctx := context.Background()

	sent, err := p.deployment.Topics().Send(ctx, &topicv1.SendRequest{Topic: "orders", Payload: []byte(exactJSON), Key: "eu", Lane: topicv1.Lane_LANE_HIGH})
	if err != nil {
		t.Fatalf("Send() = %v", err)
	}
	if !ulid.MatchString(sent.GetMessageId()) {
		t.Fatalf("Send() answered message id %q, want a bare 26-character ULID", sent.GetMessageId())
	}

	pulled := p.pull("orders", "ship")
	if len(pulled) != 1 {
		t.Fatalf("the consumer's subscription holds %d messages, want 1", len(pulled))
	}
	got := pulled[0]
	if got.payload() != exactJSON {
		t.Errorf("the message carries %s, want %s byte for byte", got.payload(), exactJSON)
	}
	if got.Message.Attributes[topics.MessageAttribute] != sent.GetMessageId() {
		t.Errorf("the message names id %q, want the %q Send() answered", got.Message.Attributes[topics.MessageAttribute], sent.GetMessageId())
	}
	if _, err := time.Parse(time.RFC3339Nano, got.Message.Attributes[topics.PublishedAtAttribute]); err != nil {
		t.Errorf("the message's publish time %q does not read as a time: %v", got.Message.Attributes[topics.PublishedAtAttribute], err)
	}
	if got.Message.Attributes[topics.LaneAttribute] != "high" {
		t.Errorf("the message names lane %q, want high", got.Message.Attributes[topics.LaneAttribute])
	}
	if _, targeted := got.Message.Attributes[topics.ConsumerAttribute]; targeted {
		t.Error("a send names a consumer, and a send reaches every consumer")
	}
}

func TestLiveASendWithALiveIdempotencyKeyAnswersTheFirstMessageAndPublishesNoOther(t *testing.T) {
	p := newPublished(t)
	ctx := context.Background()

	first, err := p.deployment.Topics().Send(ctx, &topicv1.SendRequest{Topic: "orders", Payload: []byte(`{"n":1}`), IdempotencyKey: "order-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.deployment.Topics().Send(ctx, &topicv1.SendRequest{Topic: "orders", Payload: []byte(`{"n":2}`), IdempotencyKey: "order-1"})
	if err != nil {
		t.Fatal(err)
	}
	if second.GetMessageId() != first.GetMessageId() {
		t.Errorf("the second send answered %s, want the first's %s", second.GetMessageId(), first.GetMessageId())
	}
	if pulled := p.pull("orders", "ship"); len(pulled) != 1 {
		t.Errorf("the consumer's subscription holds %d messages, want only the first", len(pulled))
	}
}

func TestLiveASendToNoDeployedTopicOrWithAPayloadThatIsNotJSONIsRefused(t *testing.T) {
	p := newPublished(t)
	ctx := context.Background()

	for name, tc := range map[string]struct {
		req  *topicv1.SendRequest
		code connect.Code
	}{
		"no such topic":      {&topicv1.SendRequest{Topic: "nothing", Payload: []byte(`{}`)}, connect.CodeNotFound},
		"a task":             {&topicv1.SendRequest{Topic: "resize", Payload: []byte(`{}`)}, connect.CodeNotFound},
		"a payload not JSON": {&topicv1.SendRequest{Topic: "orders", Payload: []byte(`{`)}, connect.CodeInvalidArgument},
	} {
		if _, err := p.deployment.Topics().Send(ctx, tc.req); connect.CodeOf(err) != tc.code {
			t.Errorf("Send() of %s = %v, want %s", name, err, tc.code)
		}
	}
}

func (p published) deliverPulled(worker *fakeWorker, topic, consumer string) []int {
	p.t.Helper()
	deliveries := topics.Deliveries{Store: p.deployment.Store(), Topics: p.deployment.Declared, Worker: worker.url}
	var codes []int
	for _, message := range p.pull(topic, consumer) {
		req := httptest.NewRequest(http.MethodPost, topics.PushPath(topic, consumer), bytes.NewReader(message.pushBody()))
		recorder := httptest.NewRecorder()
		deliveries.ServeHTTP(recorder, req)
		codes = append(codes, recorder.Code)
	}
	return codes
}

func TestLiveASendReachesEveryConsumerAsARunOfItsOwn(t *testing.T) {
	p := newPublished(t)
	worker := newFakeWorker(t, always(http.StatusOK, `{}`))

	sent, err := p.deployment.Topics().Send(context.Background(), &topicv1.SendRequest{Topic: "orders", Payload: []byte(exactJSON)})
	if err != nil {
		t.Fatal(err)
	}
	for _, consumer := range []string{"ship", "digest"} {
		codes := p.deliverPulled(worker, "orders", consumer)
		if len(codes) != 1 || !acked(codes[0]) {
			t.Errorf("consumer %s's subscription delivered %v, want one acked push", consumer, codes)
		}
		run, err := p.deployment.Store().ReadRun(context.Background(), sent.GetMessageId()+"-"+consumer)
		if err != nil || run.Status != provider.RunCompleted {
			t.Errorf("consumer %s's run = %+v, %v, want it completed", consumer, run, err)
		}
	}
	if received := worker.received(); len(received) != 2 {
		t.Errorf("the worker got %d envelopes, want one for each consumer", len(received))
	}
}
