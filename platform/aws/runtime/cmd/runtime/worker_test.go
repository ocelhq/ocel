package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/runtime/tasks"
)

func TestAWorkerReadsQueueRecordsCronFiringsAndWarmEventsAndRefusesAnythingElse(t *testing.T) {
	t.Parallel()

	engine := tasks.New(tasks.Config{Topology: queues.Topology{Topics: map[string]queues.Topic{}}})
	ctx := context.Background()

	if answer, err := workerAnswer(ctx, []byte(`{"ocel":{"warm":1}}`), engine); err != nil || string(answer) != `{}` {
		t.Errorf("a warm event = %s, %v, want an empty answer", answer, err)
	}
	records, err := json.Marshal(events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", EventSourceARN: "arn:aws:sqs:us-east-1:000000000000:elsewhere", Body: `{}`}}})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := workerAnswer(ctx, records, engine)
	if err != nil {
		t.Fatalf("workerAnswer(records) = %v", err)
	}
	var resp events.SQSEventResponse
	if err := json.Unmarshal(answer, &resp); err != nil || len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "m1" {
		t.Errorf("a record from a queue this deployment does not consume = %s, want it kept on its queue as a batch item failure", answer)
	}
	if _, err := workerAnswer(ctx, []byte(`{"ocelCron":"heartbeat","scheduledTime":"<aws.scheduler.scheduled-time>"}`), engine); err == nil || !strings.Contains(err.Error(), "heartbeat") {
		t.Errorf("a cron firing for an undeclared task = %v, want an error naming it", err)
	}
	if _, err := workerAnswer(ctx, []byte(`{"detail":{}}`), engine); err == nil {
		t.Error("an event that is neither records nor a schedule was answered, want it refused")
	}
}
