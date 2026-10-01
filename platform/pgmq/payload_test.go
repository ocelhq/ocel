package pgmq

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const exactJSON = `{"ratio":2.0,"id":9007199254740993,"count":2}`

func rawFieldOf(t *testing.T, body []byte, name string) string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("the envelope %s is not a JSON object: %v", body, err)
	}
	return string(fields[name])
}

func TestATaskPayloadAndOutputKeepTheirJSONTextFromTriggerThroughTheEnvelopeToTheRunRecord(t *testing.T) {
	worker := newWorker(t, func(*topicv1.Envelope) reply { return reply{status: http.StatusOK, body: exactJSON} })
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"resize": aTask()}, map[string]Worker{"worker": {URL: worker.server.URL}})

	id := trigger(t, engine, "resize", exactJSON, &taskv1.TriggerOptions{Metadata: []byte(exactJSON)})
	retrieved := awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)

	got := worker.received()
	if len(got) != 1 {
		t.Fatalf("the worker received %d envelopes, want 1", len(got))
	}
	if payload := rawFieldOf(t, got[0].body, "payload"); payload != exactJSON {
		t.Errorf("the envelope's payload = %s, want the triggered JSON %s inline and unchanged", payload, exactJSON)
	}
	run, err := engine.Store().ReadRun(context.Background(), id)
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if string(run.Payload) != exactJSON || string(run.Output) != exactJSON {
		t.Errorf("run record payload = %s, output = %s, want both %s", run.Payload, run.Output, exactJSON)
	}
	if string(retrieved.GetPayload()) != exactJSON || string(retrieved.GetOutput()) != exactJSON || string(retrieved.GetMetadata()) != exactJSON {
		t.Errorf("retrieved run payload = %s, output = %s, metadata = %s, want each %s", retrieved.GetPayload(), retrieved.GetOutput(), retrieved.GetMetadata(), exactJSON)
	}
}

func TestATriggerWhoseMetadataIsNotAJSONObjectIsRefused(t *testing.T) {
	engine := applied(t, map[string]*contractv1.ManifestTopic{"resize": aTask()}, nil)

	for _, metadata := range []string{`[1]`, `"tag"`, `null`, `{`} {
		_, err := engine.Tasks().Trigger(context.Background(), &taskv1.TriggerRequest{Task: "resize", Payload: []byte(`{}`), Options: &taskv1.TriggerOptions{Metadata: []byte(metadata)}})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("Trigger(metadata %s) error = %v, want an invalid argument", metadata, err)
		}
	}
}

func TestADeadLetterKeepsItsPayloadsJSONText(t *testing.T) {
	answer, err := protojson.Marshal(&topicv1.Answer{Outcome: &topicv1.Answer_Abort{Abort: &topicv1.Abort{Reason: "refused"}}})
	if err != nil {
		t.Fatal(err)
	}
	worker := newWorker(t, func(*topicv1.Envelope) reply {
		return reply{status: http.StatusUnprocessableEntity, body: string(answer)}
	})
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"orders": aTopic(&contractv1.ManifestConsumer{Name: "email", Worker: "worker"})}, map[string]Worker{"worker": {URL: worker.server.URL}})

	send(t, engine, "orders", exactJSON, nil)
	deadline := time.Now().Add(10 * time.Second)
	for countDeadLetters(t, engine) < 1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	resp, err := engine.Topics().ListDeadLetters(context.Background(), &topicv1.ListDeadLettersRequest{Topic: "orders", Consumer: "email"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetDeadLetters()) != 1 || string(resp.GetDeadLetters()[0].GetPayload()) != exactJSON {
		t.Errorf("dead letters = %v, want the one aborted message with payload %s", resp.GetDeadLetters(), exactJSON)
	}
}

func TestAMessageKeepsItsJSONTextInTheEnvelope(t *testing.T) {
	worker := newWorker(t, succeeding)
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"orders": aTopic(&contractv1.ManifestConsumer{Name: "email", Worker: "worker"})}, map[string]Worker{"worker": {URL: worker.server.URL}})

	send(t, engine, "orders", exactJSON, nil)

	got := awaitDelivered(t, worker, 1)
	if payload := rawFieldOf(t, got[0].body, "payload"); payload != exactJSON {
		t.Errorf("the envelope's payload = %s, want the sent JSON %s inline and unchanged", payload, exactJSON)
	}
}

func TestABatchedMessageKeepsItsJSONTextInTheEnvelope(t *testing.T) {
	worker := newWorker(t, succeeding)
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"orders": aTopic(batching(1, time.Second))}, map[string]Worker{"worker": {URL: worker.server.URL}})

	send(t, engine, "orders", exactJSON, nil)

	deadline := time.Now().Add(10 * time.Second)
	for len(worker.received()) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	got := worker.received()
	if len(got) == 0 {
		t.Fatal("the worker received no batch")
	}
	var deliveries []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rawFieldOf(t, got[0].body, "messages")), &deliveries); err != nil || len(deliveries) != 1 {
		t.Fatalf("the batch envelope %s carries no one delivery: %v", got[0].body, err)
	}
	if payload := string(deliveries[0]["payload"]); payload != exactJSON {
		t.Errorf("the delivery's payload = %s, want the sent JSON %s inline and unchanged", payload, exactJSON)
	}
}

func TestARunWhoseOutputIsOver256KiBFailsOnceWithoutARetry(t *testing.T) {
	oversized := `"` + strings.Repeat("a", 256<<10) + `"`
	worker := newWorker(t, func(*topicv1.Envelope) reply { return reply{status: http.StatusOK, body: oversized} })
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"resize": aTask(retrying(3, time.Millisecond, time.Millisecond))}, map[string]Worker{"worker": {URL: worker.server.URL}})

	id := trigger(t, engine, "resize", `{}`, nil)
	run := awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_FAILED)

	if run.GetAttempts() != 1 || len(worker.received()) != 1 {
		t.Errorf("the run took %d attempts and the worker %d envelopes, want one of each", run.GetAttempts(), len(worker.received()))
	}
	if len(run.GetOutput()) != 0 || !strings.Contains(run.GetError(), "256 KiB") {
		t.Errorf("run output %d bytes, error %q, want no output and an error naming the 256 KiB limit", len(run.GetOutput()), run.GetError())
	}
}
