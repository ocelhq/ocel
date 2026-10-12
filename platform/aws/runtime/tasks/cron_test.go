package tasks

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

type triggeringTable struct {
	leasingTable
	transacted []*dynamodb.TransactWriteItemsInput
}

func (t *triggeringTable) TransactWriteItems(_ context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	t.transacted = append(t.transacted, in)
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

func carries(item map[string]ddbtypes.AttributeValue, text string) bool {
	var decoded map[string]any
	if err := attributevalue.UnmarshalMap(item, &decoded); err != nil {
		return false
	}
	return strings.Contains(fmt.Sprint(decoded), text)
}

func TestACronMarkerArrivingOnATasksQueueStartsTheTaskOnceForItsMinute(t *testing.T) {
	t.Parallel()

	db := &triggeringTable{}
	queue := &sentMessages{}
	consumer := provider.ConsumerSpec{Name: "nightly", Worker: "media", Exclusive: true}
	declared := &provider.TopicSpec{Cron: "0 3 * * *", Consumers: []provider.ConsumerSpec{consumer}}
	e := New(Config{Table: db, Queues: queue, Worker: "media", Topology: queues.Topology{
		Table: "state", KeyPrefix: "p#",
		Topics: map[string]queues.Topic{"nightly": {Declared: declared, Queues: map[string]string{"nightly": "q-nightly"}}},
	}})

	response := e.Deliver(context.Background(), events.SQSEvent{Records: []events.SQSMessage{{
		MessageId:      "m-cron",
		Body:           `{"ocelCron":"nightly","scheduledTime":"2026-10-12T03:00:41Z"}`,
		EventSourceARN: "arn:aws:sqs:::q-nightly",
	}}})

	if len(response.BatchItemFailures) != 0 {
		t.Errorf("Deliver() kept %v, want the marker acknowledged once its task is triggered", response.BatchItemFailures)
	}
	if len(queue.sent) != 1 || !strings.HasSuffix(*queue.sent[0].QueueUrl, "q-nightly") {
		t.Fatalf("sent %d messages, want the one run of nightly on its queue", len(queue.sent))
	}
	var keyed bool
	for _, put := range db.puts {
		if carries(put.Item, "cron-2026-10-12T03:00:00Z") {
			keyed = true
		}
	}
	for _, written := range db.transacted {
		for _, item := range written.TransactItems {
			if item.Put != nil && carries(item.Put.Item, "cron-2026-10-12T03:00:00Z") {
				keyed = true
			}
		}
	}
	if !keyed {
		t.Errorf("no write carries the idempotency key cron-2026-10-12T03:00:00Z, so a schedule that fires twice in a minute would run the task twice")
	}
}
